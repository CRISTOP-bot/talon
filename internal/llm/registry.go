package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/talon-cli/talon/internal/errs"
)

// Factory builds a provider from options.
type Factory func(Options) Provider

// DefaultBaseURLs maps provider names to their API roots. Users can override
// any of them with model.base_url, which is how self-hosted servers are used.
var DefaultBaseURLs = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"anthropic":  "https://api.anthropic.com/v1",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta",
	"openrouter": "https://openrouter.ai/api/v1",
	"ollama":     "http://localhost:11434/v1",
	"llamacpp":   "http://localhost:8080/v1",
}

// IsOpenAICompatible reports whether a provider speaks the OpenAI protocol,
// which decides how requests are encoded.
func IsOpenAICompatible(provider string) bool {
	switch provider {
	case "openai", "openrouter", "ollama", "llamacpp", "custom", "mock":
		return true
	}
	return false
}

// openAICompatible adapts any OpenAI-protocol provider under a custom name so
// `talon config set model.provider my-gateway` works without code changes.
// NewCompatible wraps the OpenAI protocol under an arbitrary provider name.
func NewCompatible(name string, opts Options) Provider {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		return nil
	}
	p := NewOpenAI(opts)
	return &namedProvider{Provider: p, name: name}
}

// namedProvider overrides the reported name of a provider.
type namedProvider struct {
	Provider
	name string
}

func (n *namedProvider) Name() string { return n.name }

// New builds a provider by name. Unknown names fall back to the OpenAI
// protocol against the configured base URL, which is what self-hosted gateways
// need.
func New(provider string, opts Options) (Provider, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURLs[provider]
	}
	switch provider {
	case "anthropic":
		return NewAnthropic(opts), nil
	case "gemini":
		return NewGemini(opts), nil
	case "mock":
		return NewMock(opts), nil
	case "openai", "openrouter", "ollama", "llamacpp", "custom", "":
		return NewOpenAI(opts), nil
	default:
		e := errs.Config("llm", "unknown provider %q", provider)
		e.Hint = "run `talon models` to see the supported providers"
		return nil, e
	}
}

// Providers lists the supported provider identifiers.
func Providers() []string {
	out := append([]string(nil), keys(DefaultBaseURLs)...)
	out = append(out, "custom", "mock")
	sort.Strings(out)
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Catalog returns the default model list for a provider without making a
// network call. It is what `talon models` shows offline.
func Catalog(provider string) []ModelInfo {
	models := DefaultModelsFor(provider)
	SortModels(models)
	return models
}

// DescribeProvider returns a one-line description used by `talon doctor`.
func DescribeProvider(provider string) string {
	base, ok := DefaultBaseURLs[provider]
	if !ok {
		base = "(configured base_url)"
	}
	return fmt.Sprintf("%s — %s", provider, base)
}

// IsLocalProvider reports whether a provider normally runs on this machine.
func IsLocalProvider(provider string) bool {
	switch provider {
	case "ollama", "llamacpp", "mock":
		return true
	}
	return false
}

// needsKey reports whether a provider requires an API key.
func needsKey(provider string) bool {
	return !IsLocalProvider(provider)
}

// Validate checks that the options are usable before a request is made.
func Validate(provider string, opts Options) error {
	if provider == "" {
		return errs.Config("llm", "no provider configured")
	}
	if needsKey(provider) && opts.APIKey == "" {
		e := errs.Config("llm", "no API key for provider %q", provider)
		e.Hint = "export AI_API_KEY=... or run `talon init`"
		return e
	}
	if opts.Model == "" && provider != "mock" {
		e := errs.Config("llm", "no model configured for provider %q", provider)
		e.Hint = "run `talon models` to choose one"
		return e
	}
	if IsLocalProvider(provider) && opts.Model != "" {
		return nil
	}
	if strings.TrimSpace(opts.BaseURL) == "" && DefaultBaseURLs[provider] == "" {
		e := errs.Config("llm", "no base URL for provider %q", provider)
		e.Hint = "set model.base_url in the configuration"
		return e
	}
	return nil
}

// HealthCheck performs a cheap round trip to verify credentials and
// connectivity. It is used by `talon doctor --network`.
func HealthCheck(ctx context.Context, p Provider) error {
	_, err := p.ListModels(ctx)
	if err != nil {
		return err
	}
	return nil
}
