package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/errs"
)

// Factory builds a provider from options.
type Factory func(Options) Provider

// DefaultBaseURLs maps provider names to their API roots. It is generated from
// the catalogue in providers.go, so adding a provider there is enough.
var DefaultBaseURLs = func() map[string]string {
	out := make(map[string]string, len(providerSpecs))
	for name, spec := range providerSpecs {
		out[name] = spec.BaseURL
	}
	return out
}()

// IsOpenAICompatible reports whether a provider speaks the OpenAI protocol,
// which decides how requests are encoded.
func IsOpenAICompatible(provider string) bool {
	spec, ok := Spec(provider)
	if !ok {
		// An unknown name is assumed to be an OpenAI-compatible endpoint, which
		// is the common case for self-hosted servers.
		return true
	}
	return spec.Protocol == ProtocolOpenAI
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
	p, err := build(provider, opts)
	if err != nil {
		return nil, err
	}
	// The configured model is applied here so no caller can forget it.
	return WithDefaultModel(p, opts.Model), nil
}

func build(provider string, opts Options) (Provider, error) {
	switch provider {
	case "anthropic":
		return NewAnthropic(opts), nil
	case "gemini":
		return NewGemini(opts), nil
	case "mock":
		return NewMock(opts), nil
	case "":
		return NewOpenAI(opts), nil
	default:
		// Every catalogue provider that is not Anthropic or Gemini speaks the
		// OpenAI protocol, which covers OpenRouter, NVIDIA, Groq and the rest.
		if spec, ok := Spec(provider); ok {
			switch spec.Protocol {
			case ProtocolAnthropic:
				return NewAnthropic(opts), nil
			case ProtocolGemini:
				return NewGemini(opts), nil
			case ProtocolOpenAI:
				return NewOpenAI(opts), nil
			}
		}
		e := errs.Config("llm", "unknown provider %q", provider)
		e.Hint = "run `talon models` to see the supported providers"
		return nil, e
	}
}

// Providers lists the supported provider identifiers.
func Providers() []string {
	out := make([]string, 0, len(providerSpecs))
	for name := range providerSpecs {
		out = append(out, name)
	}
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
	if spec, ok := Spec(provider); ok && len(spec.Models) > 0 {
		models := append([]ModelInfo(nil), spec.Models...)
		SortModels(models)
		return models
	}
	models := DefaultModelsFor(provider)
	SortModels(models)
	return models
}

// DescribeProvider returns a one-line description used by `talon doctor`.
func DescribeProvider(provider string) string {
	spec, ok := Spec(provider)
	if !ok {
		return fmt.Sprintf("%s — (configured base_url)", provider)
	}
	base := spec.BaseURL
	if base == "" {
		base = "(configured base_url)"
	}
	return fmt.Sprintf("%s — %s", provider, base)
}

// IsLocalProvider reports whether a provider normally runs on this machine.
func IsLocalProvider(provider string) bool {
	spec, ok := Spec(provider)
	return ok && spec.Local
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
