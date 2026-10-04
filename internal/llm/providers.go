package llm

import "sort"

// Protocol is the wire protocol a provider speaks.
type Protocol string

// Protocols Talon knows how to speak.
const (
	// ProtocolOpenAI is the /chat/completions protocol most providers expose.
	ProtocolOpenAI Protocol = "openai"
	// ProtocolAnthropic is the Messages API.
	ProtocolAnthropic Protocol = "anthropic"
	// ProtocolGemini is generateContent.
	ProtocolGemini Protocol = "gemini"
	// ProtocolNone means no HTTP is involved.
	ProtocolNone Protocol = "none"
)

// ProviderSpec is everything Talon needs to talk to one provider: where it lives,
// which environment variables hold its key, which protocol it speaks and which
// models it offers by default.
//
// Keeping this in one place is what stops the pieces from drifting: the network
// policy reads the host from here, the configuration reads the key variable from
// here, and `talon models` lists the catalogue from here.
type ProviderSpec struct {
	Name string
	// BaseURL is the API root, overridable with model.base_url.
	BaseURL string
	// KeyEnv lists the environment variables checked for a key, in order.
	KeyEnv []string
	// Protocol decides how requests are encoded.
	Protocol Protocol
	// Local marks providers that normally run on this machine: no key needed and
	// loopback access allowed.
	Local bool
	// Note is a short, honest description for `talon models` and `talon doctor`.
	Note string
	// Models is the offline catalogue shown before any network call.
	Models []ModelInfo
}

// Spec returns the definition of a provider.
func Spec(provider string) (ProviderSpec, bool) {
	s, ok := providerSpecs[provider]
	return s, ok
}

// Specs returns every provider, sorted by name.
func Specs() []ProviderSpec {
	out := make([]ProviderSpec, 0, len(providerSpecs))
	for _, s := range providerSpecs {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// KeyEnvFor returns the environment variables that may hold the provider's key,
// including the generic one, so a single AI_API_KEY keeps working everywhere.
func KeyEnvFor(provider string) []string {
	s, ok := providerSpecs[provider]
	if !ok {
		return []string{"AI_API_KEY", "OPENAI_API_KEY"}
	}
	envs := append([]string(nil), s.KeyEnv...)
	envs = append(envs, "AI_API_KEY")
	return envs
}

// IsLocal reports whether the provider normally runs on this machine.
func (s ProviderSpec) IsLocal() bool { return s.Local }

// needsKey reports whether a provider requires an API key.
func (s ProviderSpec) needsKey() bool { return !s.Local && s.Protocol != ProtocolNone }

// model is a small helper for building catalogues without noise.
func model(id string, ctx int) ModelInfo {
	return ModelInfo{ID: id, ContextWindow: ctx}
}

// providerSpecs is the catalogue. Every entry here is also a host the network
// policy must allow: a provider that cannot be reached is worse than one that is
// never offered.
var providerSpecs = map[string]ProviderSpec{
	"openai": {
		Name:     "openai",
		BaseURL:  "https://api.openai.com/v1",
		KeyEnv:   []string{"OPENAI_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "GPT models, ChatGPT Plus or an API key",
		Models: []ModelInfo{
			model("gpt-5", 400000), model("gpt-5-codex", 400000), model("gpt-4.1", 1000000),
			model("gpt-4o", 128000), model("o4-mini", 200000),
		},
	},
	"anthropic": {
		Name:     "anthropic",
		BaseURL:  "https://api.anthropic.com/v1",
		KeyEnv:   []string{"ANTHROPIC_API_KEY"},
		Protocol: ProtocolAnthropic,
		Note:     "Claude models",
		Models: []ModelInfo{
			model("claude-opus-4-5", 200000), model("claude-sonnet-4-5", 200000),
			model("claude-haiku-4-5", 200000),
		},
	},
	"gemini": {
		Name:     "gemini",
		BaseURL:  "https://generativelanguage.googleapis.com/v1beta",
		KeyEnv:   []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		Protocol: ProtocolGemini,
		Note:     "Google Gemini models",
		Models: []ModelInfo{
			model("gemini-2.5-pro", 1000000), model("gemini-2.5-flash", 1000000),
			model("gemini-2.0-flash", 1000000),
		},
	},
	"openrouter": {
		Name:     "openrouter",
		BaseURL:  "https://openrouter.ai/api/v1",
		KeyEnv:   []string{"OPENROUTER_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "one key, many providers and models",
		Models: []ModelInfo{
			model("anthropic/claude-sonnet-4.5", 200000),
			model("openai/gpt-5", 400000),
			model("google/gemini-2.5-pro", 1000000),
			model("qwen/qwen3-coder", 262144),
			model("deepseek/deepseek-r1", 128000),
		},
	},
	"nvidia": {
		Name:     "nvidia",
		BaseURL:  "https://integrate.api.nvidia.com/v1",
		KeyEnv:   []string{"NVIDIA_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "NVIDIA NIM models, including Nemotron and Qwen",
		Models: []ModelInfo{
			model("nvidia/llama-3.3-nemotron-super-49b", 131072),
			model("qwen/qwen3-coder-480b-a35b-instruct", 262144),
			model("deepseek-ai/deepseek-r1", 128000),
			model("meta/llama-3.3-70b-instruct", 131072),
		},
	},
	"groq": {
		Name:     "groq",
		BaseURL:  "https://api.groq.com/openai/v1",
		KeyEnv:   []string{"GROQ_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "very fast inference on open models",
		Models: []ModelInfo{
			model("moonshotai/kimi-k2-instruct", 131072),
			model("llama-3.3-70b-versatile", 131072),
			model("qwen3-32b", 131072),
		},
	},
	"together": {
		Name:     "together",
		BaseURL:  "https://api.together.xyz/v1",
		KeyEnv:   []string{"TOGETHER_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "open models, including Qwen and DeepSeek",
		Models: []ModelInfo{
			model("Qwen/Qwen3-Coder-480B-A35B-Instruct", 262144),
			model("meta-llama/Llama-3.3-70B-Instruct-Turbo", 131072),
			model("deepseek-ai/DeepSeek-V3", 128000),
		},
	},
	"deepseek": {
		Name:     "deepseek",
		BaseURL:  "https://api.deepseek.com/v1",
		KeyEnv:   []string{"DEEPSEEK_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "DeepSeek chat and reasoning models",
		Models:   []ModelInfo{model("deepseek-chat", 128000), model("deepseek-reasoner", 128000)},
	},
	"mistral": {
		Name:     "mistral",
		BaseURL:  "https://api.mistral.ai/v1",
		KeyEnv:   []string{"MISTRAL_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "Mistral and Codestral models",
		Models: []ModelInfo{
			model("mistral-large-latest", 131072),
			model("codestral-latest", 262144),
			model("devstral-medium-latest", 131072),
		},
	},
	"fireworks": {
		Name:     "fireworks",
		BaseURL:  "https://api.fireworks.ai/inference/v1",
		KeyEnv:   []string{"FIREWORKS_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "Fireworks hosted open models",
		Models: []ModelInfo{
			model("accounts/fireworks/models/qwen3-coder-480b-a35b-instruct", 262144),
			model("accounts/fireworks/models/deepseek-v3", 128000),
		},
	},
	"cerebras": {
		Name:     "cerebras",
		BaseURL:  "https://api.cerebras.ai/v1",
		KeyEnv:   []string{"CEREBRAS_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "fast inference on open models",
		Models: []ModelInfo{
			model("qwen-3-coder-480b", 262144),
			model("llama-3.3-70b", 131072),
			model("gpt-oss-120b", 131072),
		},
	},
	"xai": {
		Name:     "xai",
		BaseURL:  "https://api.x.ai/v1",
		KeyEnv:   []string{"XAI_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "Grok models",
		Models:   []ModelInfo{model("grok-code-fast-1", 256000), model("grok-4", 256000)},
	},
	"perplexity": {
		Name:     "perplexity",
		BaseURL:  "https://api.perplexity.ai",
		KeyEnv:   []string{"PERPLEXITY_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "Sonar models with live search",
		Models: []ModelInfo{
			model("sonar", 127072), model("sonar-pro", 200000), model("sonar-reasoning", 127072),
		},
	},
	"siliconflow": {
		Name:     "siliconflow",
		BaseURL:  "https://api.siliconflow.cn/v1",
		KeyEnv:   []string{"SILICONFLOW_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "Chinese hosted models, cheap and fast",
		Models: []ModelInfo{
			model("deepseek-ai/DeepSeek-V3", 128000),
			model("Qwen/Qwen3-Coder-480B-A35B-Instruct", 262144),
		},
	},
	"huggingface": {
		Name:     "huggingface",
		BaseURL:  "https://router.huggingface.co/v1",
		KeyEnv:   []string{"HF_TOKEN", "HUGGINGFACE_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "the Hugging Face inference router",
		Models: []ModelInfo{
			model("deepseek-ai/DeepSeek-V3-0324", 128000),
			model("Qwen/Qwen3-Coder-480B-A35B-Instruct", 262144),
		},
	},
	"github": {
		Name:     "github",
		BaseURL:  "https://models.github.ai/inference",
		KeyEnv:   []string{"GITHUB_TOKEN"},
		Protocol: ProtocolOpenAI,
		Note:     "GitHub Models, with your GitHub token",
		Models: []ModelInfo{
			model("openai/gpt-4.1", 1000000),
			model("deepseek/DeepSeek-R1", 128000),
		},
	},
	"lmstudio": {
		Name:     "lmstudio",
		BaseURL:  "http://localhost:1234/v1",
		KeyEnv:   []string{},
		Protocol: ProtocolOpenAI,
		Local:    true,
		Note:     "LM Studio on this machine",
		Models:   []ModelInfo{},
	},
	"ollama": {
		Name:     "ollama",
		BaseURL:  "http://localhost:11434/v1",
		KeyEnv:   []string{},
		Protocol: ProtocolOpenAI,
		Local:    true,
		Note:     "Ollama on this machine",
		Models:   []ModelInfo{},
	},
	"llamacpp": {
		Name:     "llamacpp",
		BaseURL:  "http://localhost:8080/v1",
		KeyEnv:   []string{},
		Protocol: ProtocolOpenAI,
		Local:    true,
		Note:     "llama.cpp server on this machine",
		Models:   []ModelInfo{},
	},
	"custom": {
		Name:     "custom",
		BaseURL:  "",
		KeyEnv:   []string{"AI_API_KEY"},
		Protocol: ProtocolOpenAI,
		Note:     "any OpenAI-compatible endpoint; set model.base_url",
		Models:   []ModelInfo{},
	},
	"mock": {
		Name:     "mock",
		BaseURL:  "",
		KeyEnv:   []string{},
		Protocol: ProtocolNone,
		Local:    true,
		Note:     "offline provider used by the tests",
		Models:   []ModelInfo{},
	},
}
