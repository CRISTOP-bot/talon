package llm

import (
	"net/url"
	"strings"
	"testing"
)

// TestCatalogueIsConsistent catches the mistakes that are easy to make when
// adding a provider by hand: a missing key variable, a key on a local provider,
// a base URL that is not a URL.
func TestCatalogueIsConsistent(t *testing.T) {
	for _, spec := range Specs() {
		if spec.Name == "" {
			t.Fatal("a provider has no name")
		}
		if spec.Protocol == "" {
			t.Errorf("%s: no protocol", spec.Name)
		}
		switch spec.Protocol {
		case ProtocolOpenAI, ProtocolAnthropic, ProtocolGemini, ProtocolNone:
		default:
			t.Errorf("%s: unknown protocol %q", spec.Name, spec.Protocol)
		}
		if spec.Local && len(spec.KeyEnv) > 0 {
			t.Errorf("%s: a local provider should not require a key", spec.Name)
		}
		if !spec.Local && spec.Protocol != ProtocolNone && len(spec.KeyEnv) == 0 {
			t.Errorf("%s: a hosted provider needs a key variable", spec.Name)
		}
		if spec.BaseURL != "" {
			u, err := url.Parse(spec.BaseURL)
			if err != nil || u.Host == "" {
				t.Errorf("%s: base URL %q is not usable", spec.Name, spec.BaseURL)
			}
		}
		for _, env := range spec.KeyEnv {
			if env != strings.ToUpper(env) {
				t.Errorf("%s: environment variable %q should be upper case", spec.Name, env)
			}
		}
	}
}

// TestTheProvidersPeopleAskForExist is a deliberate inventory test: these are the
// providers the README and the issue tracker mention by name.
func TestTheProvidersPeopleAskForExist(t *testing.T) {
	want := []string{
		"openai", "anthropic", "gemini", "openrouter", "nvidia", "groq",
		"together", "deepseek", "mistral", "fireworks", "cerebras", "xai",
		"perplexity", "siliconflow", "huggingface", "github",
		"ollama", "llamacpp", "lmstudio", "custom", "mock",
	}
	have := map[string]bool{}
	for _, s := range Specs() {
		have[s.Name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("provider %q is missing from the catalogue", name)
		}
	}
}

func TestProvidersListsEverything(t *testing.T) {
	listed := map[string]bool{}
	for _, name := range Providers() {
		listed[name] = true
	}
	for _, spec := range Specs() {
		if !listed[spec.Name] {
			t.Errorf("Providers() does not list %q", spec.Name)
		}
	}
}

func TestKeyEnvIncludesTheGenericVariable(t *testing.T) {
	envs := KeyEnvFor("nvidia")
	if len(envs) < 2 || envs[0] != "NVIDIA_API_KEY" {
		t.Fatalf("NVIDIA should read NVIDIA_API_KEY first, got %v", envs)
	}
	if envs[len(envs)-1] != "AI_API_KEY" {
		t.Errorf("AI_API_KEY must remain a fallback, got %v", envs)
	}
}

func TestLocalProvidersAreRecognised(t *testing.T) {
	for _, name := range []string{"ollama", "llamacpp", "lmstudio", "mock"} {
		if !IsLocalProvider(name) {
			t.Errorf("%s should be local", name)
		}
	}
	for _, name := range []string{"openai", "nvidia", "openrouter", "groq"} {
		if IsLocalProvider(name) {
			t.Errorf("%s should not be local", name)
		}
	}
}

func TestUnknownProviderIsRejected(t *testing.T) {
	if _, ok := Spec("not-a-provider"); ok {
		t.Fatal("an unknown provider must not resolve")
	}
	if _, err := New("not-a-provider", Options{}); err == nil {
		t.Fatal("building an unknown provider must fail with a clear error")
	}
}

func TestEveryCataloguedProviderCanBeBuilt(t *testing.T) {
	for _, spec := range Specs() {
		if spec.Protocol == ProtocolNone {
			continue
		}
		p, err := New(spec.Name, Options{APIKey: "test-key"})
		if err != nil {
			t.Errorf("%s: %v", spec.Name, err)
			continue
		}
		if p == nil {
			t.Errorf("%s: no provider was built", spec.Name)
		}
	}
}

func TestNonOpenAIProtocolsKeepTheirProtocol(t *testing.T) {
	for _, name := range []string{"anthropic", "gemini"} {
		spec, _ := Spec(name)
		if IsOpenAICompatible(name) {
			t.Errorf("%s does not speak the OpenAI protocol", name)
		}
		if spec.Protocol == ProtocolOpenAI {
			t.Errorf("%s is catalogued as OpenAI-compatible", name)
		}
	}
	for _, name := range []string{"nvidia", "openrouter", "groq", "xai"} {
		if !IsOpenAICompatible(name) {
			t.Errorf("%s should be treated as OpenAI-compatible", name)
		}
	}
}

func TestCatalogueHasModelsForHostedProviders(t *testing.T) {
	for _, spec := range Specs() {
		// "custom" is whatever the user points it at, so there is nothing to
		// list before the first request.
		if spec.Local || spec.Protocol == ProtocolNone || spec.Name == "custom" {
			continue
		}
		if len(spec.Models) == 0 {
			t.Errorf("%s: `talon models` would show nothing without a network call", spec.Name)
		}
		for _, m := range spec.Models {
			if m.ID == "" {
				t.Errorf("%s: a catalogue entry has no model id", spec.Name)
			}
			if m.ContextWindow <= 0 {
				t.Errorf("%s/%s: no context window", spec.Name, m.ID)
			}
		}
	}
}
