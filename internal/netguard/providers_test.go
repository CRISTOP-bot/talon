package netguard

import (
	"testing"

	"github.com/CRISTOP-bot/talon/internal/llm"
)

// TestEveryCatalogueProviderIsReachable is the test that matters for this
// package: a provider the allowlist does not know about is a provider Talon
// silently cannot use, and the failure looks like a network problem.
func TestEveryCatalogueProviderIsReachable(t *testing.T) {
	for _, spec := range llm.Specs() {
		if spec.BaseURL == "" || spec.Local {
			continue
		}
		p := DefaultPolicy()
		for _, host := range ProviderHosts(spec.Name, spec.BaseURL) {
			p.Allow(host)
		}
		if err := p.CheckHost(t.Context(), spec.BaseURL+"/models"); err != nil {
			t.Errorf("%s: the allowlist blocks its own API: %v", spec.Name, err)
		}
	}
}

func TestProviderHostsCoverTheBaseURL(t *testing.T) {
	for _, spec := range llm.Specs() {
		if spec.BaseURL == "" {
			continue
		}
		hosts := ProviderHosts(spec.Name, spec.BaseURL)
		if len(hosts) == 0 {
			t.Errorf("%s: no host was derived", spec.Name)
		}
	}
}

func TestLocalProvidersAreRecognised(t *testing.T) {
	for _, name := range []string{"ollama", "llamacpp", "lmstudio", "mock"} {
		if !IsLocalProviderHost(name) {
			t.Errorf("%s runs on this machine and must be allowed on loopback", name)
		}
	}
	for _, name := range []string{"openai", "nvidia", "groq"} {
		if IsLocalProviderHost(name) {
			t.Errorf("%s is hosted and must not be treated as local", name)
		}
	}
}

// TestLocalProvidersStillRefuseRemoteAddresses proves allowing loopback for a
// local provider does not open the network.
func TestLocalProvidersStillRefuseRemoteAddresses(t *testing.T) {
	p := DefaultPolicy()
	// This mirrors what internal/secure does for a local model server.
	p.AllowLoopback = IsLocalProviderHost("ollama")
	p.AllowHTTP = IsLocalProviderHost("ollama")
	p.Allow("localhost", "127.0.0.1")
	if err := p.CheckHost(t.Context(), "http://127.0.0.1:11434/v1/chat/completions"); err != nil {
		t.Fatalf("a local provider must reach its own server: %v", err)
	}
	if err := p.CheckHost(t.Context(), "http://93.184.216.34/v1"); err == nil {
		t.Fatal("a hosted address must stay blocked even for a local provider")
	}
}
