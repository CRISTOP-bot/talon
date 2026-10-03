package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CRISTOP-bot/talon/internal/app"
	"github.com/CRISTOP-bot/talon/internal/errs"
)

// Provider is the interface every model backend implements.
type Provider interface {
	// Name is the provider identifier used in configuration.
	Name() string
	// ListModels returns the models the provider offers.
	ListModels(ctx context.Context) ([]ModelInfo, error)
	// Stream sends a request and delivers events to onEvent as they arrive.
	// It returns an error only for transport-level failures; model-level
	// errors are delivered as EventError followed by a return of nil.
	Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error
	// Complete performs a non-streaming request.
	Complete(ctx context.Context, req Request) (Response, error)
}

// Options configure a provider instance.
type Options struct {
	// HTTPClient, when set, is used as-is. Talon passes a client built by
	// internal/netguard so every request passes the network policy.
	HTTPClient *http.Client
	APIKey     string
	BaseURL    string
	Model      string
	Timeout    time.Duration
	MaxRetries int
	Headers    map[string]string
	// MaxOutputTokens is the default when a request does not set one.
	MaxOutputTokens int
	// APIVersion is the protocol header value Anthropic requires.
	APIVersion string
}

// client wraps http.Client with retry, auth and error mapping helpers shared by
// the HTTP providers.
type client struct {
	opts     Options
	http     *http.Client
	userInfo string
}

func newClient(opts Options) *client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 0} // per-request deadlines control streaming
	}
	return &client{opts: opts, http: hc, userInfo: app.UserAgent()}
}

// withRetry runs fn, retrying transient failures with exponential backoff.
func (c *client) withRetry(ctx context.Context, fn func() error) error {
	attempts := c.opts.MaxRetries
	if attempts < 0 {
		attempts = 0
	}
	var lastErr error
	for attempt := 0; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return errs.Cancelled("llm", "request cancelled")
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !errs.Retryable(lastErr) || attempt == attempts {
			return lastErr
		}
		wait := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
		if delay := retryAfter(lastErr); delay > 0 {
			wait = delay
		}
		select {
		case <-ctx.Done():
			return errs.Cancelled("llm", "request cancelled")
		case <-time.After(wait):
		}
	}
	return lastErr
}

// retryHint carries a Retry-After duration inside an error.
type retryHint struct {
	err  error
	wait time.Duration
}

func (r *retryHint) Error() string { return r.err.Error() }
func (r *retryHint) Unwrap() error { return r.err }

func retryAfter(err error) time.Duration {
	var rh *retryHint
	if errors.As(err, &rh) {
		return rh.wait
	}
	return 0
}

// do performs an HTTP request with auth headers and JSON decoding.
func (c *client) do(ctx context.Context, method, url string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return errs.Internal("llm", "cannot encode request: %v", err)
		}
	}
	return c.withRetry(ctx, func() error {
		var reader io.Reader
		if payload != nil {
			reader = strings.NewReader(string(payload))
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return errs.Internal("llm", "cannot build request: %v", err)
		}
		req.Header.Set("User-Agent", c.userInfo)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.opts.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
		}
		for k, v := range c.opts.Headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return classifyTransportError(ctx, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return statusError(resp)
		}
		if out == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return errs.Parse("llm", "the provider returned a malformed response: %v", err)
		}
		return nil
	})
}

// openStream performs a streaming HTTP request and returns the response.
func (c *client) openStream(ctx context.Context, url string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errs.Internal("llm", "cannot encode request: %v", err)
	}
	var resp *http.Response
	err = c.withRetry(ctx, func() error {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
		if rerr != nil {
			return errs.Internal("llm", "cannot build request: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("User-Agent", c.userInfo)
		if c.opts.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
		}
		for k, v := range c.opts.Headers {
			req.Header.Set(k, v)
		}
		r, derr := c.http.Do(req)
		if derr != nil {
			return classifyTransportError(ctx, derr)
		}
		if r.StatusCode >= 400 {
			defer r.Body.Close()
			return statusError(r)
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// sseEvent is one server-sent event.
type sseEvent struct {
	name string
	data string
}

// readSSE streams server-sent events from a response body.
func readSSE(ctx context.Context, body io.Reader, fn func(sseEvent) error) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var name, data strings.Builder
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if data.Len() > 0 {
				ev := sseEvent{name: name.String(), data: data.String()}
				name.Reset()
				data.Reset()
				if err := fn(ev); err != nil {
					return err
				}
			}
		case strings.HasPrefix(line, ":"):
			// keep-alive comment
		case strings.HasPrefix(line, "event:"):
			name.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimPrefix(line, "data:"))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := sc.Err(); err != nil {
		return errs.Network("llm", "the connection was interrupted: %v", err)
	}
	if data.Len() > 0 {
		return fn(sseEvent{name: name.String(), data: data.String()})
	}
	return nil
}

// classifyTransportError maps a transport failure onto the error taxonomy.
func classifyTransportError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return errs.Cancelled("llm", "request cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errs.New(errs.KindTimeout, "llm", "the request timed out")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return errs.New(errs.KindTimeout, "llm", "the request timed out")
	case strings.Contains(msg, "connection refused"):
		e := errs.Newf(errs.KindNetwork, "llm", "cannot reach the provider: %v", err)
		e.Hint = "check AI_BASE_URL and that the server is running"
		return e
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "dns"):
		return errs.Newf(errs.KindNetwork, "llm", "cannot resolve the provider host: %v", err)
	default:
		return errs.Newf(errs.KindNetwork, "llm", "network error: %v", err)
	}
}

// statusError maps an HTTP error response onto the error taxonomy, extracting
// the provider's message when possible.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	msg := strings.TrimSpace(string(body))
	detail := extractErrorMessage(msg)
	kind := errs.KindNetwork
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		kind = errs.KindAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		kind = errs.KindRateLimit
	case resp.StatusCode == http.StatusNotFound:
		kind = errs.KindNotFound
	case resp.StatusCode == http.StatusBadRequest:
		kind = errs.KindModel
	case resp.StatusCode >= 500:
		kind = errs.KindNetwork
	}
	e := errs.Newf(kind, "llm", "%s (HTTP %d)", detail, resp.StatusCode)
	switch kind {
	case errs.KindAuth:
		e.Hint = "check AI_API_KEY; it may be missing, wrong or expired"
	case errs.KindRateLimit:
		e.Hint = "the provider is rate limiting requests; wait a moment or lower agent.max_steps"
	case errs.KindNotFound:
		e.Hint = "check that the model name exists for this provider (run `talon models`)"
	case errs.KindModel:
		e.Hint = "the request was rejected by the provider; check model parameters and tool schemas"
	}
	if kind == errs.KindRateLimit {
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil {
				return &retryHint{err: e, wait: time.Duration(secs) * time.Second}
			}
		}
	}
	return e
}

// extractErrorMessage pulls the human-readable message out of a provider error
// payload, which differs per vendor.
func extractErrorMessage(body string) string {
	if body == "" {
		return "the provider returned an error"
	}
	var generic struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &generic); err == nil {
		if generic.Error.Message != "" {
			return generic.Error.Message
		}
		if generic.Message != "" {
			return generic.Message
		}
	}
	// Fall back to a trimmed single line.
	flat := strings.Join(strings.Fields(body), " ")
	if len(flat) > 300 {
		flat = flat[:300] + "…"
	}
	return flat
}

// accumulator rebuilds a Response from a stream of events. Providers emit
// complete tool calls (not argument deltas), so accumulation is additive.
type accumulator struct {
	mu        sync.Mutex
	text      strings.Builder
	reasoning strings.Builder
	calls     []ToolCall
	usage     Usage
	stop      StopReason
	model     string
}

// Accumulate applies an event, returning true when the stream is complete.
func (a *accumulator) Accumulate(ev StreamEvent) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch ev.Type {
	case EventText:
		a.text.WriteString(ev.Text)
	case EventReasoning:
		a.reasoning.WriteString(ev.Text)
	case EventToolCall:
		if ev.ToolCall != nil {
			a.calls = append(a.calls, *ev.ToolCall)
		}
	case EventUsage:
		if ev.Usage != nil {
			a.usage = *ev.Usage
		}
	case EventDone:
		a.stop = ev.StopReason
		if ev.Response != nil {
			a.model = ev.Response.Model
		}
		return true
	}
	return false
}

// Build assembles the final response.
func (a *accumulator) Build() Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	msg := Message{
		Role:      RoleAssistant,
		Content:   a.text.String(),
		Reasoning: a.reasoning.String(),
		ToolCalls: append([]ToolCall(nil), a.calls...),
	}
	return Response{Message: msg, Usage: a.usage, StopReason: a.stop, Model: a.model}
}

// Complete implements Provider.Complete on top of Stream.
func Complete(ctx context.Context, p Provider, req Request) (Response, error) {
	acc := &accumulator{}
	var streamErr error
	err := p.Stream(ctx, req, func(ev StreamEvent) error {
		if ev.Type == EventError {
			streamErr = ev.Err
			return errStopStream
		}
		acc.Accumulate(ev)
		return nil
	})
	if streamErr != nil {
		return Response{}, streamErr
	}
	if err != nil && !errors.Is(err, errStopStream) {
		return Response{}, err
	}
	return acc.Build(), nil
}

// errStopStream ends a stream early without reporting failure.
var errStopStream = errors.New("stop stream")

// CollectStream runs a provider stream and returns the assembled response.
// It is used by tests and by the non-interactive `talon run` mode.
func CollectStream(ctx context.Context, p Provider, req Request, onEvent func(StreamEvent) error) (Response, error) {
	acc := &accumulator{}
	var out Response
	err := p.Stream(ctx, req, func(ev StreamEvent) error {
		if ev.Type == EventError {
			return ev.Err
		}
		if acc.Accumulate(ev) {
			out = acc.Build()
		}
		if onEvent != nil {
			return onEvent(ev)
		}
		return nil
	})
	if err != nil {
		return Response{}, err
	}
	if out.Message.Content == "" && len(out.Message.ToolCalls) == 0 && out.StopReason == "" {
		out = acc.Build()
	}
	return out, nil
}

// ResolveTimeout converts a seconds value into a duration with a sane default.
func ResolveTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// ErrNoResponse is returned when a provider produced nothing at all.
var ErrNoResponse = errs.New(errs.KindParse, "llm", "the provider returned an empty response")
