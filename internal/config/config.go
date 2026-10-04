// Package config loads, merges and persists Talon's configuration.
//
// Precedence (low to high):
//
//	built-in defaults  <  ~/.config/talon/config.toml
//	                   <  .talon/config.toml in the project
//	                   <  environment variables
//
// The merged document is retained so `talon config set` can rewrite a single
// key without discarding the comments a user wrote by hand.
package config

import (
	"github.com/CRISTOP-bot/talon/internal/llm"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/toml"
)

// Permission levels, ordered from most to least restricted.
const (
	LevelReadOnly = "read-only"
	LevelSafe     = "safe"
	LevelConfirm  = "confirm"
	LevelFull     = "full-access"
)

// ProjectDirName is the per-project configuration directory.
const ProjectDirName = ".talon"

// Model holds LLM connection and sampling settings.
type Model struct {
	Provider       string
	Name           string
	BaseURL        string
	APIKey         string
	APIKeyEnv      string
	Temperature    float64
	TopP           float64
	MaxTokens      int
	TimeoutSeconds int
	ExtraHeaders   map[string]string
	// MaxRetries applies to transient failures (network, 429, 5xx).
	MaxRetries int
}

// Agent controls the agent loop.
type Agent struct {
	MaxSteps         int
	MaxToolOutput    int
	SystemPromptFile string
	AutoApproveTools []string
}

// Context controls what is sent to the model.
type Context struct {
	MaxBytes         int
	MaxFiles         int
	RespectGitignore bool
	IncludeGitStatus bool
	MaxFileBytes     int
	// AutoCompact summarises old turns when the history approaches the limit.
	AutoCompact bool
	// CompactAt is the fraction of the context window that triggers compaction.
	CompactAt float64
}

// UI holds presentation settings.
type UI struct {
	Theme        string
	Spinner      bool
	Stream       bool
	ShowThinking bool
	Syntax       bool
}

// Permissions controls tool approval.
type Permissions struct {
	Level          string
	AllowPaths     []string
	DenyPaths      []string
	DenyCommands   []string
	AllowCommands  []string
	TimeoutSeconds int
}

// Memory configures persistent project memory.
type Memory struct {
	Enabled  bool
	MaxNotes int
}

// Sessions configures transcript persistence.
type Sessions struct {
	Enabled  bool
	Autosave bool
	Max      int
}

// Logging configures optional debug logs.
type Logging struct {
	Level  string
	File   string
	Redact bool
}

// PluginEntry describes one installed or configured plugin.
type PluginEntry struct {
	Name       string
	Command    string
	Args       []string
	Enabled    bool
	Env        map[string]string
	ToolPrefix string
}

// Plugins configures external plugin processes.
type Plugins struct {
	Enabled bool
	Dir     string
	Entries []PluginEntry
}

// MCPEntry describes an MCP server to connect over stdio.
type MCPEntry struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	Enabled bool
}

// MCP configures Model Context Protocol servers.
type MCP struct {
	Enabled bool
	Servers []MCPEntry
}

// Security holds the security posture. Every default is the safe one: the
// fields are set so that turning a control off is always a deliberate act.
type Security struct {
	// CommandConfirmation requires approval before a shell command runs.
	CommandConfirmation bool
	// Sandbox enables kernel isolation for child processes where available.
	Sandbox bool
	// SandboxRequired refuses to run commands at all when no kernel sandbox is
	// available, instead of falling back to unconfined execution.
	SandboxRequired bool
	// NetworkMode is "allowlist", "ask" or "off".
	NetworkMode string
	// NetworkConfirmation asks before contacting a host outside the allow list.
	NetworkConfirmation bool
	// AllowedDomains are extra hosts Talon may contact.
	AllowedDomains []string
	// AllowHTTP permits plain http (local model servers).
	AllowHTTP bool
	// AllowLoopback and AllowPrivateIPs are for local model servers.
	AllowLoopback   bool
	AllowPrivateIPs bool
	// SecretRedaction scrubs credentials from logs, errors and outbound content.
	SecretRedaction bool
	// SensitiveFiles is "block", "mask", "warn" or "allow".
	SensitiveFiles string
	// ContentFindings is the same set, applied to content being sent.
	ContentFindings string
	// AllowSensitivePaths re-enables specific credential files.
	AllowSensitivePaths []string
	// AuditEnabled records security decisions locally.
	AuditEnabled bool
	// AuditLevel is "minimal", "normal" or "verbose".
	AuditLevel string
	// AuditPath overrides the audit log location.
	AuditPath string
	// Telemetry is always false: Talon has no telemetry. The key exists so the
	// absence is explicit and any future change is visible.
	Telemetry bool
	// PrivacyMode disables persistence, logs and the audit log.
	PrivacyMode bool
	// RequireTerms makes an unaccepted Terms version block agent use.
	RequireTerms bool
	// MaxFileBytes caps how large a file may be before it is refused.
	MaxFileBytes int64
}

// Updater controls self-update checks.
type Updater struct {
	Channel      string
	Repo         string
	CheckOnStart bool
}

// Config is the fully resolved configuration for one CLI run.
type Config struct {
	// ProjectRoot is the directory the CLI was started in.
	ProjectRoot string
	// UserFile and ProjectFile are the files that contributed ("" if absent).
	UserFile    string
	ProjectFile string

	Model       Model
	Agent       Agent
	Context     Context
	UI          UI
	Permissions Permissions
	Memory      Memory
	Sessions    Sessions
	Logging     Logging
	Plugins     Plugins
	MCP         MCP
	Security    Security
	Updater     Updater

	// doc is the merged document (user + project); userDoc and projDoc are
	// the individual files so writes never leak project values into the user
	// file and vice versa.
	doc     *toml.Value
	userDoc *toml.Value
	projDoc *toml.Value
	// warnings collected while loading (unknown keys, bad values...).
	Warnings []string
}

// Defaults returns the built-in configuration with every field populated.
func Defaults() *Config {
	c := &Config{doc: toml.NewTable(), userDoc: toml.NewTable(), projDoc: toml.NewTable()}
	c.Model = Model{
		Provider:       "openai",
		Name:           "",
		BaseURL:        "",
		APIKeyEnv:      "AI_API_KEY",
		Temperature:    0.2,
		TopP:           1.0,
		MaxTokens:      8192,
		TimeoutSeconds: 120,
		MaxRetries:     3,
		ExtraHeaders:   map[string]string{},
	}
	c.Agent = Agent{
		MaxSteps:         24,
		MaxToolOutput:    20000,
		AutoApproveTools: []string{},
	}
	c.Context = Context{
		MaxBytes:         180000,
		MaxFiles:         60,
		RespectGitignore: true,
		IncludeGitStatus: true,
		MaxFileBytes:     400000,
		AutoCompact:      true,
		CompactAt:        0.75,
	}
	c.UI = UI{Theme: "default", Spinner: true, Stream: true, ShowThinking: true, Syntax: true}
	c.Permissions = Permissions{
		Level:          LevelConfirm,
		AllowPaths:     []string{},
		DenyPaths:      []string{},
		DenyCommands:   defaultDenyCommands(),
		AllowCommands:  []string{},
		TimeoutSeconds: 600,
	}
	c.Memory = Memory{Enabled: true, MaxNotes: 200}
	c.Sessions = Sessions{Enabled: true, Autosave: true, Max: 200}
	c.Logging = Logging{Level: "warn", File: paths.LogFile(), Redact: true}
	c.Plugins = Plugins{Enabled: true, Dir: paths.PluginDir(), Entries: []PluginEntry{}}
	c.MCP = MCP{Enabled: false, Servers: []MCPEntry{}}
	c.Security = Security{
		CommandConfirmation: true,
		Sandbox:             true,
		SandboxRequired:     false,
		NetworkMode:         "allowlist",
		NetworkConfirmation: true,
		AllowedDomains:      []string{},
		AllowHTTP:           false,
		AllowLoopback:       false,
		AllowPrivateIPs:     false,
		SecretRedaction:     true,
		SensitiveFiles:      "block",
		ContentFindings:     "mask",
		AllowSensitivePaths: []string{},
		AuditEnabled:        true,
		AuditLevel:          "normal",
		Telemetry:           false,
		PrivacyMode:         false,
		RequireTerms:        true,
		MaxFileBytes:        524288,
	}
	c.Updater = Updater{Channel: "stable", Repo: appRepo, CheckOnStart: false}
	return c
}

// appRepo is the repository used by `talon update` and `talon plugins install`.
const appRepo = "CRISTOP-bot/talon"

// defaultDenyCommands is the baseline blocklist. It is intentionally small:
// the permission engine is conservative for anything not on the allow list.
func defaultDenyCommands() []string {
	return []string{
		"rm -rf /",
		"mkfs",
		"dd if=",
		":(){",
		"shutdown",
		"reboot",
		"chown -R /",
		"git push --force",
		"curl ... | sh",
		"wget ... | sh",
	}
}

// Load reads configuration for the given project root.
func Load(projectRoot string) (*Config, error) {
	c := Defaults()
	c.ProjectRoot = projectRoot

	if dir := os.Getenv(paths.EnvConfigDir); dir != "" {
		c.UserFile = filepath.Join(dir, "config.toml")
	} else {
		c.UserFile = paths.ConfigFile()
	}
	c.ProjectFile = filepath.Join(projectRoot, ProjectDirName, "config.toml")

	userDoc, err := loadFile(c.UserFile)
	if err != nil {
		return nil, err
	}
	projDoc, err := loadFile(c.ProjectFile)
	if err != nil {
		return nil, err
	}
	if userDoc != nil {
		merge(c.userDoc, userDoc)
	}
	if projDoc != nil {
		merge(c.projDoc, projDoc)
	}
	merge(c.doc, c.userDoc)
	merge(c.doc, c.projDoc)

	if err := c.derive(); err != nil {
		return nil, err
	}
	c.applyEnv()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func loadFile(path string) (*toml.Value, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errs.WrapHint(errs.KindPermission, "config",
			"check that the file exists and is readable", err)
	}
	doc, perr := toml.Parse(string(data))
	if perr != nil {
		return nil, errs.WrapHint(errs.KindParse, "config",
			"check the file syntax; see docs/configuration.md", perr)
	}
	return doc, nil
}

// merge deep-merges src into dst: tables are merged, scalars are replaced.
func merge(dst, src *toml.Value) {
	if src == nil || src.Kind != toml.KindTable {
		return
	}
	for _, k := range src.Keys() {
		sv := src.Table[k]
		dv, ok := dst.Get(k)
		if ok && dv.Kind == toml.KindTable && sv.Kind == toml.KindTable {
			merge(dv, sv)
			continue
		}
		dst.Set(k, sv)
	}
}

// derive fills the typed fields from the merged document, recording a warning
// for values that could not be interpreted.
func (c *Config) derive() error {
	d := c.doc
	c.Warnings = nil
	c.Model.Provider = str(d, "model.provider", c.Model.Provider)
	c.Model.Name = str(d, "model.name", c.Model.Name)
	c.Model.BaseURL = str(d, "model.base_url", c.Model.BaseURL)
	c.Model.APIKey = str(d, "model.api_key", c.Model.APIKey)
	c.Model.APIKeyEnv = str(d, "model.api_key_env", c.Model.APIKeyEnv)
	c.Model.Temperature = flt(d, "model.temperature", c.Model.Temperature)
	c.Model.TopP = flt(d, "model.top_p", c.Model.TopP)
	c.Model.MaxTokens = num(d, "model.max_tokens", c.Model.MaxTokens)
	c.Model.TimeoutSeconds = num(d, "model.timeout_seconds", c.Model.TimeoutSeconds)
	c.Model.MaxRetries = num(d, "model.max_retries", c.Model.MaxRetries)
	hv, _ := d.Get("model.extra_headers")
	c.Model.ExtraHeaders = headers(hv)

	c.Agent.MaxSteps = num(d, "agent.max_steps", c.Agent.MaxSteps)
	c.Agent.MaxToolOutput = num(d, "agent.max_tool_output", c.Agent.MaxToolOutput)
	c.Agent.SystemPromptFile = str(d, "agent.system_prompt_file", c.Agent.SystemPromptFile)
	c.Agent.AutoApproveTools = list(d, "agent.auto_approve_tools", c.Agent.AutoApproveTools)

	c.Context.MaxBytes = num(d, "context.max_bytes", c.Context.MaxBytes)
	c.Context.MaxFiles = num(d, "context.max_files", c.Context.MaxFiles)
	c.Context.RespectGitignore = boolean(d, "context.respect_gitignore", c.Context.RespectGitignore)
	c.Context.IncludeGitStatus = boolean(d, "context.include_git_status", c.Context.IncludeGitStatus)
	c.Context.MaxFileBytes = num(d, "context.max_file_bytes", c.Context.MaxFileBytes)
	c.Context.AutoCompact = boolean(d, "context.auto_compact", c.Context.AutoCompact)
	c.Context.CompactAt = flt(d, "context.compact_at", c.Context.CompactAt)

	c.UI.Theme = str(d, "ui.theme", c.UI.Theme)
	c.UI.Spinner = boolean(d, "ui.spinner", c.UI.Spinner)
	c.UI.Stream = boolean(d, "ui.stream", c.UI.Stream)
	c.UI.ShowThinking = boolean(d, "ui.show_thinking", c.UI.ShowThinking)
	c.UI.Syntax = boolean(d, "ui.syntax", c.UI.Syntax)

	c.Permissions.Level = normalizeLevel(str(d, "permissions.level", c.Permissions.Level))
	c.Permissions.AllowPaths = list(d, "permissions.allow_paths", c.Permissions.AllowPaths)
	c.Permissions.DenyPaths = list(d, "permissions.deny_paths", c.Permissions.DenyPaths)
	c.Permissions.DenyCommands = list(d, "permissions.deny_commands", c.Permissions.DenyCommands)
	c.Permissions.AllowCommands = list(d, "permissions.allow_commands", c.Permissions.AllowCommands)
	c.Permissions.TimeoutSeconds = num(d, "permissions.timeout_seconds", c.Permissions.TimeoutSeconds)

	c.Memory.Enabled = boolean(d, "memory.enabled", c.Memory.Enabled)
	c.Memory.MaxNotes = num(d, "memory.max_notes", c.Memory.MaxNotes)

	c.Sessions.Enabled = boolean(d, "sessions.enabled", c.Sessions.Enabled)
	c.Sessions.Autosave = boolean(d, "sessions.autosave", c.Sessions.Autosave)
	c.Sessions.Max = num(d, "sessions.max", c.Sessions.Max)

	c.Logging.Level = str(d, "log.level", c.Logging.Level)
	c.Logging.File = str(d, "log.file", c.Logging.File)
	c.Logging.Redact = boolean(d, "log.redact", c.Logging.Redact)

	c.Plugins.Enabled = boolean(d, "plugins.enabled", c.Plugins.Enabled)
	c.Plugins.Dir = str(d, "plugins.dir", c.Plugins.Dir)
	c.Plugins.Entries = pluginEntries(d)

	c.MCP.Enabled = boolean(d, "mcp.enabled", c.MCP.Enabled)
	c.MCP.Servers = mcpServers(d)

	c.Security.CommandConfirmation = boolean(d, "security.command_confirmation", c.Security.CommandConfirmation)
	c.Security.Sandbox = boolean(d, "security.sandbox", c.Security.Sandbox)
	c.Security.SandboxRequired = boolean(d, "security.sandbox_required", c.Security.SandboxRequired)
	c.Security.NetworkMode = str(d, "security.network_mode", c.Security.NetworkMode)
	c.Security.NetworkConfirmation = boolean(d, "security.network_confirmation", c.Security.NetworkConfirmation)
	c.Security.AllowedDomains = list(d, "security.allowed_domains", c.Security.AllowedDomains)
	c.Security.AllowHTTP = boolean(d, "security.allow_http", c.Security.AllowHTTP)
	c.Security.AllowLoopback = boolean(d, "security.allow_loopback", c.Security.AllowLoopback)
	c.Security.AllowPrivateIPs = boolean(d, "security.allow_private_ips", c.Security.AllowPrivateIPs)
	c.Security.SecretRedaction = boolean(d, "security.secret_redaction", c.Security.SecretRedaction)
	c.Security.SensitiveFiles = str(d, "security.sensitive_files", c.Security.SensitiveFiles)
	c.Security.ContentFindings = str(d, "security.content_findings", c.Security.ContentFindings)
	c.Security.AllowSensitivePaths = list(d, "security.allow_sensitive_paths", c.Security.AllowSensitivePaths)
	c.Security.AuditEnabled = boolean(d, "security.audit_enabled", c.Security.AuditEnabled)
	c.Security.AuditLevel = str(d, "security.audit_level", c.Security.AuditLevel)
	c.Security.AuditPath = str(d, "security.audit_path", c.Security.AuditPath)
	c.Security.Telemetry = boolean(d, "security.telemetry", c.Security.Telemetry)
	c.Security.PrivacyMode = boolean(d, "security.privacy_mode", c.Security.PrivacyMode)
	c.Security.RequireTerms = boolean(d, "security.require_terms", c.Security.RequireTerms)
	c.Security.MaxFileBytes = int64(num(d, "security.max_file_bytes", int(c.Security.MaxFileBytes)))

	c.Updater.Channel = str(d, "update.channel", c.Updater.Channel)
	c.Updater.Repo = str(d, "update.repo", c.Updater.Repo)
	c.Updater.CheckOnStart = boolean(d, "update.check_on_start", c.Updater.CheckOnStart)

	if c.Model.Provider == "" {
		return errs.Config("config", "model.provider must not be empty")
	}
	if c.Model.Name == "" && c.Plugins.Enabled {
		c.Warnings = append(c.Warnings, "model.name is empty: run `talon models` to pick a model")
	}
	return nil
}

func headers(v *toml.Value) map[string]string {
	out := map[string]string{}
	if v == nil || v.Kind != toml.KindTable {
		return out
	}
	for _, k := range v.Keys() {
		if s, ok := v.Table[k].AsString(); ok {
			out[k] = s
		}
	}
	return out
}

func pluginEntries(d *toml.Value) []PluginEntry {
	v, ok := d.Get("plugins.entries")
	if !ok || v.Kind != toml.KindArray {
		return []PluginEntry{}
	}
	out := make([]PluginEntry, 0, len(v.Array))
	for _, e := range v.Array {
		if e == nil || e.Kind != toml.KindTable {
			continue
		}
		pe := PluginEntry{ToolPrefix: "plugin_" + fieldString(e, "name")}
		pe.Name = fieldString(e, "name")
		pe.Command = fieldString(e, "command")
		pe.Args = fieldStrings(e, "args")
		if b, ok := fieldBool(e, "enabled"); ok {
			pe.Enabled = b
		} else {
			pe.Enabled = true
		}
		pe.Env = headers(e.Table["env"])
		if pe.ToolPrefix == "plugin_" {
			pe.ToolPrefix = "plugin"
		}
		out = append(out, pe)
	}
	return out
}

func mcpServers(d *toml.Value) []MCPEntry {
	v, ok := d.Get("mcp.servers")
	if !ok || v.Kind != toml.KindArray {
		return []MCPEntry{}
	}
	out := make([]MCPEntry, 0, len(v.Array))
	for _, e := range v.Array {
		if e == nil || e.Kind != toml.KindTable {
			continue
		}
		s := MCPEntry{
			Name:    fieldString(e, "name"),
			Command: fieldString(e, "command"),
			Args:    fieldStrings(e, "args"),
			Env:     headers(e.Table["env"]),
		}
		if b, ok := fieldBool(e, "enabled"); ok {
			s.Enabled = b
		} else {
			s.Enabled = true
		}
		if s.Name == "" || s.Command == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func fieldString(t *toml.Value, k string) string {
	if t == nil {
		return ""
	}
	v, ok := t.Get(k)
	if !ok {
		return ""
	}
	s, _ := v.AsString()
	return s
}

func fieldBool(t *toml.Value, k string) (bool, bool) {
	if t == nil {
		return false, false
	}
	v, ok := t.Get(k)
	if !ok {
		return false, false
	}
	return v.AsBool()
}

func fieldStrings(t *toml.Value, k string) []string {
	if t == nil {
		return nil
	}
	v, ok := t.Get(k)
	if !ok {
		return nil
	}
	s, ok := v.AsStrings()
	if !ok {
		return nil
	}
	return s
}

func str(d *toml.Value, path, def string) string {
	v, ok := d.Get(path)
	if !ok {
		return def
	}
	s, ok := v.AsString()
	if !ok {
		return def
	}
	return s
}

func num(d *toml.Value, path string, def int) int {
	v, ok := d.Get(path)
	if !ok {
		return def
	}
	n, ok := v.AsInt()
	if !ok {
		return def
	}
	return int(n)
}

func flt(d *toml.Value, path string, def float64) float64 {
	v, ok := d.Get(path)
	if !ok {
		return def
	}
	f, ok := v.AsFloat()
	if !ok {
		return def
	}
	return f
}

func boolean(d *toml.Value, path string, def bool) bool {
	v, ok := d.Get(path)
	if !ok {
		return def
	}
	b, ok := v.AsBool()
	if !ok {
		return def
	}
	return b
}

func list(d *toml.Value, path string, def []string) []string {
	v, ok := d.Get(path)
	if !ok {
		return def
	}
	s, ok := v.AsStrings()
	if !ok {
		return def
	}
	return s
}

// normalizeLevel maps the aliases users are used to onto canonical levels.
func normalizeLevel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read-only", "readonly", "ro":
		return LevelReadOnly
	case "safe":
		return LevelSafe
	case "confirm", "interactive", "ask":
		return LevelConfirm
	case "full-access", "full", "auto", "yolo":
		return LevelFull
	default:
		return s
	}
}

// withHint attaches a recovery hint to a freshly built error.
func withHint(e *errs.Error, hint string) *errs.Error {
	e.Hint = hint
	return e
}

// Validate exposes configuration checks for `talon doctor`.
// Validate checks the resolved configuration. It is exported so callers that
// adjust values at runtime (command-line flags, tests) can re-check them.
func (c *Config) Validate() error { return c.validate() }

func (c *Config) validate() error {
	switch c.Permissions.Level {
	case LevelReadOnly, LevelSafe, LevelConfirm, LevelFull:
	default:
		return withHint(errs.Config("config", "unknown permissions.level %q", c.Permissions.Level),
			"valid levels: read-only, safe, confirm, full-access")
	}
	if c.Agent.MaxSteps < 1 {
		return errs.Config("config", "agent.max_steps must be at least 1")
	}
	if c.Model.Temperature < 0 || c.Model.Temperature > 2 {
		return errs.Config("config", "model.temperature must be between 0 and 2")
	}
	if c.Model.TimeoutSeconds < 1 {
		return errs.Config("config", "model.timeout_seconds must be at least 1")
	}
	if c.Model.Provider != "" {
		if _, known := llm.Spec(c.Model.Provider); !known {
			return withHint(errs.Config("config", "model.provider %q is not a known provider", c.Model.Provider),
				"run `talon models` to list the supported providers")
		}
	}
	switch c.Security.NetworkMode {
	case "allowlist", "ask", "off":
	default:
		return withHint(errs.Config("config", "security.network_mode %q is not valid", c.Security.NetworkMode),
			"use allowlist, ask or off")
	}
	for _, action := range []string{c.Security.SensitiveFiles, c.Security.ContentFindings} {
		switch action {
		case "block", "mask", "warn", "allow":
		default:
			return withHint(errs.Config("config", "security action %q is not valid", action),
				"use block, mask, warn or allow")
		}
	}
	switch c.Security.AuditLevel {
	case "minimal", "normal", "verbose", "off":
	default:
		return withHint(errs.Config("config", "security.audit_level %q is not valid", c.Security.AuditLevel),
			"use minimal, normal, verbose or off")
	}
	if c.Security.Telemetry {
		return withHint(errs.Config("config", "security.telemetry cannot be enabled"),
			"this build contains no telemetry code path; the key exists so the absence is explicit")
	}
	return nil
}

// ApplyEnv overrides configuration from the environment.
func (c *Config) applyEnv() {
	set := func(env, path string, val string) {
		if val == "" {
			return
		}
		c.doc.Set(path, toml.String(val))
	}
	set("AI_PROVIDER", "model.provider", os.Getenv("AI_PROVIDER"))
	set("AI_MODEL", "model.name", os.Getenv("AI_MODEL"))
	set("AI_BASE_URL", "model.base_url", os.Getenv("AI_BASE_URL"))
	set("AI_TEMPERATURE", "model.temperature", os.Getenv("AI_TEMPERATURE"))
	set("AI_MAX_TOKENS", "model.max_tokens", os.Getenv("AI_MAX_TOKENS"))

	// Model name may also be given as provider/model (OpenRouter style).
	// The provider is only rewritten when AI_PROVIDER was not set explicitly.
	if v := os.Getenv("AI_MODEL"); strings.Contains(v, "/") && !strings.Contains(v, " ") && os.Getenv("AI_PROVIDER") == "" {
		parts := strings.SplitN(v, "/", 2)
		c.doc.Set("model.provider", toml.String(parts[0]))
		set("AI_MODEL", "model.name", parts[1])
	}

	if v := os.Getenv("TALON_PERMISSIONS"); v != "" {
		c.doc.Set("permissions.level", toml.String(normalizeLevel(v)))
	}
	if v := os.Getenv("TALON_THEME"); v != "" {
		c.doc.Set("ui.theme", toml.String(v))
	}
	if v := os.Getenv("TALON_LOG_LEVEL"); v != "" {
		c.doc.Set("log.level", toml.String(v))
	}
	if v := os.Getenv("TALON_MAX_STEPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.doc.Set("agent.max_steps", toml.Int(int64(n)))
		}
	}
	// Re-derive typed values after env overrides.
	_ = c.derive()
	_ = c.validate()
}

// APIKeyValue resolves the effective API key: an explicit config value, or the
// environment variable named by model.api_key_env.
func (c *Config) APIKeyValue() string {
	if c.Model.APIKey != "" {
		return c.Model.APIKey
	}
	name := c.Model.APIKeyEnv
	if name == "" {
		name = "AI_API_KEY"
	}
	if v := os.Getenv(name); v != "" {
		return v
	}
	// Local providers need nothing; asking for a key would be noise.
	if spec, ok := llm.Spec(c.Model.Provider); ok && spec.Local {
		return ""
	}
	// Each provider has its own environment variable, so switching from
	// OpenRouter to NVIDIA does not mean rewriting the config.
	for _, name := range llm.KeyEnvFor(c.Model.Provider) {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		return v
	}
	return ""
}

// Doc exposes the merged document (used by `talon config`).
func (c *Config) Doc() *toml.Value { return c.doc }

// Set stores a raw TOML value at a dotted path in the user document and
// re-derives the config. Use SetIn to target the project document instead.
func (c *Config) Set(path, raw string) error { return c.SetIn(path, raw, "user") }

// SetIn stores a raw TOML value at a dotted path in the given scope, which is
// either "user" or "project".
func (c *Config) SetIn(path, raw, scope string) error {
	if strings.TrimSpace(path) == "" {
		return errs.Usage("a key path is required, e.g. `talon config set model.name gpt-5`")
	}
	v, err := toml.Parse("value = " + raw)
	if err != nil {
		return withHint(errs.Parse("config", "cannot interpret %q as a value", raw),
			"quote strings: `talon config set model.name \"gpt-5\"`")
	}
	val, _ := v.Get("value")
	if val == nil {
		return errs.Parse("config", "cannot interpret %q as a value", raw)
	}
	target := c.userDoc
	if scope == "project" {
		target = c.projDoc
	}
	target.Set(path, val)
	c.remerge()
	if err := c.derive(); err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	return nil
}

// UnsetIn removes a key from the given scope.
func (c *Config) UnsetIn(path, scope string) error {
	target := c.userDoc
	if scope == "project" {
		target = c.projDoc
	}
	if !target.Has(path) {
		return errs.NotFound("config", "key %q is not set", path)
	}
	target.Delete(path)
	c.remerge()
	if err := c.derive(); err != nil {
		return err
	}
	return c.validate()
}

// remerge rebuilds the merged document from the two scopes.
func (c *Config) remerge() {
	fresh := toml.NewTable()
	merge(fresh, c.userDoc)
	merge(fresh, c.projDoc)
	c.doc = fresh
}

// SaveUser writes the merged document back to the user config file with 0600
// permissions, since it may contain an API key.
func (c *Config) SaveUser() error {
	if err := paths.EnsureDir(filepath.Dir(c.UserFile)); err != nil {
		return errs.Wrap(errs.KindPermission, "config", err)
	}
	body := "# Talon configuration\n# Docs: https://github.com/" + c.Updater.Repo + "/blob/main/docs/configuration.md\n\n"
	body += string(toml.Marshal(c.userDoc))
	if err := os.WriteFile(c.UserFile, []byte(body), 0o600); err != nil {
		return errs.Wrap(errs.KindPermission, "config", err)
	}
	return nil
}

// SaveProject writes the document to the project-local config file.
func (c *Config) SaveProject() error {
	dir := filepath.Join(c.ProjectRoot, ProjectDirName)
	if err := paths.EnsureDir(dir); err != nil {
		return errs.Wrap(errs.KindPermission, "config", err)
	}
	body := string(toml.Marshal(c.projDoc))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		return errs.Wrap(errs.KindPermission, "config", err)
	}
	return nil
}

// EffectiveModel is the provider/model pair the agent will use.
func (c *Config) EffectiveModel() string {
	if c.Model.Name == "" {
		return c.Model.Provider
	}
	return c.Model.Provider + "/" + c.Model.Name
}

// KnownProviders lists the provider identifiers built into the binary.
func KnownProviders() []string {
	out := []string{"openai", "anthropic", "gemini", "openrouter", "ollama", "llamacpp", "custom"}
	sort.Strings(out)
	return out
}

// LevelRank maps a permission level to its restrictiveness (lower = stricter).
func LevelRank(level string) int {
	switch level {
	case LevelReadOnly:
		return 0
	case LevelSafe:
		return 1
	case LevelConfirm:
		return 2
	case LevelFull:
		return 3
	default:
		return 2
	}
}
