package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureProvider records the request it was given.
type captureProvider struct {
	model string
	seen  Request
}

func (c *captureProvider) Name() string { return "capture" }

func (c *captureProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }

func (c *captureProvider) Stream(_ context.Context, req Request, _ func(StreamEvent) error) error {
	c.seen = req
	return nil
}

func (c *captureProvider) Complete(_ context.Context, req Request) (Response, error) {
	c.seen = req
	return Response{}, nil
}

// TestConfiguredModelReachesTheWire is the regression test for a bug that made
// every real provider reject the request with HTTP 400 "No models provided":
// the agent builds a neutral Request without a model, and nothing filled it in
// from the configuration. The offline provider ignores the field, so the bug was
// invisible until a real API was involved.
func TestConfiguredModelReachesTheWire(t *testing.T) {
	inner := &captureProvider{}
	p := WithDefaultModel(inner, "anthropic/claude-sonnet-4.5")

	if err := p.Stream(context.Background(), Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if inner.seen.Model != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("the configured model did not reach the provider: %q", inner.seen.Model)
	}

	if _, err := p.Complete(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if inner.seen.Model != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("Complete did not fill in the model: %q", inner.seen.Model)
	}
}

func TestExplicitRequestModelWins(t *testing.T) {
	inner := &captureProvider{}
	p := WithDefaultModel(inner, "configured/model")
	_ = p.Stream(context.Background(), Request{Model: "explicit/model"}, nil)
	if inner.seen.Model != "explicit/model" {
		t.Fatalf("a request that names a model must not be overridden: %q", inner.seen.Model)
	}
}

func TestWithDefaultModelIsANoOpWithoutAModel(t *testing.T) {
	inner := &captureProvider{}
	if got := WithDefaultModel(inner, ""); got != Provider(inner) {
		t.Fatal("an empty configured model must not wrap the provider")
	}
	if got := WithDefaultModel(nil, "m"); got != nil {
		t.Fatal("a nil provider must stay nil")
	}
}

func TestWrappingIsNotAppliedTwice(t *testing.T) {
	p := WithDefaultModel(&captureProvider{}, "a/b")
	if _, wrapped := p.(defaultModelProvider); !wrapped {
		t.Fatal("expected a wrapped provider")
	}
	// Wrapping an already-wrapped provider must return it untouched, or the
	// defaults would nest and the inner one would win.
	again := WithDefaultModel(p, "a/b")
	if again != p {
		t.Fatal("the provider was wrapped twice")
	}
	inner := again.(defaultModelProvider).Provider
	if _, nested := inner.(defaultModelProvider); nested {
		t.Fatal("the wrapper contains another wrapper")
	}
}

// TestNewAppliesTheConfiguredModel checks the factory itself, because that is
// where the wiring can silently regress.
func TestNewAppliesTheConfiguredModel(t *testing.T) {
	p, err := New("mock", Options{Model: "configured/model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(defaultModelProvider); !ok {
		t.Fatalf("New did not apply the configured model, got %T", p)
	}
}

// TestOpenAIWireIncludesTheModel guards the actual JSON body, which is what the
// provider reads.
func TestOpenAIWireIncludesTheModel(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New("openai", Options{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "anthropic/claude-sonnet-4.5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stream(context.Background(), Request{}, nil); err != nil {
		t.Fatal(err)
	}
	model, _ := captured["model"].(string)
	if model != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("the request body carried model=%q", model)
	}
}

func TestProviderNameSurvivesWrapping(t *testing.T) {
	p, err := New("openai", Options{BaseURL: "http://example.invalid", APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(p.Name(), "openai") {
		t.Fatalf("Name() = %q, want openai", p.Name())
	}
}
