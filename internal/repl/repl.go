// Package repl implements Talon's interactive session: the readline loop, the
// rendering of agent events, permission prompts and the slash commands.
package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/agent"
	"github.com/CRISTOP-bot/talon/internal/config"
	buildcontext "github.com/CRISTOP-bot/talon/internal/context"
	"github.com/CRISTOP-bot/talon/internal/diff"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/git"
	"github.com/CRISTOP-bot/talon/internal/index"
	"github.com/CRISTOP-bot/talon/internal/journal"
	"github.com/CRISTOP-bot/talon/internal/legal"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/logger"
	"github.com/CRISTOP-bot/talon/internal/mcp"
	"github.com/CRISTOP-bot/talon/internal/memory"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/plugin"
	"github.com/CRISTOP-bot/talon/internal/project"
	"github.com/CRISTOP-bot/talon/internal/secure"
	"github.com/CRISTOP-bot/talon/internal/session"
	"github.com/CRISTOP-bot/talon/internal/shell"
	"github.com/CRISTOP-bot/talon/internal/term"
	"github.com/CRISTOP-bot/talon/internal/tools"
	"github.com/CRISTOP-bot/talon/internal/ui"
)

// Options configure the REPL.
type Options struct {
	Config    *config.Config
	Workspace string
	Stdout    io.Writer
	Stderr    io.Writer
	Stdin     *os.File
	Log       *logger.Logger
	// NonInteractive disables the loop: one prompt, then exit.
	NonInteractive bool
	// AutoApprove answers every confirmation with yes.
	AutoApprove bool
	// Width is the terminal width used for wrapping; 0 picks a default.
	Width int
	// Provider overrides the configured LLM provider. Embedders and tests use
	// this; when nil the provider is built from the configuration.
	Provider llm.Provider
	// ExtraTools are registered after the built-ins (used by embedders).
	ExtraTools []*tools.Definition
	// PrivacyMode disables persistence, logs and the audit log for this run.
	PrivacyMode bool
	// ConfirmNetwork is asked before contacting a host outside the allow list.
	ConfirmNetwork func(host string) error
	// SessionID correlates audit records for this run.
	SessionID string
	// InitialPrompt is executed before the loop starts.
	InitialPrompt string
}

// REPL is one interactive or non-interactive Talon session.
type REPL struct {
	opts  Options
	cfg   *config.Config
	pr    *ui.Printer
	theme ui.Theme

	agent    *agent.Agent
	tools    *tools.Registry
	toolCtx  *tools.Context
	index    *index.Index
	project  *project.Info
	memory   *memory.Store
	journal  *journal.Journal
	shell    *shell.Runner
	git      *git.Client
	sessions *session.Store

	rl      *term.Readline
	spinner *ui.Spinner
	stream  *ui.MarkdownStream

	// pendingDiffs holds the diffs produced during the current turn so /diff can
	// show them afterwards.
	pendingDiffs []diff.FileDiff
	// exitCh closes when the user asks to leave.
	exitCh  chan struct{}
	exiting bool
	// currentTurnCancel stops a running turn.
	currentTurnCancel context.CancelFunc
	// lastPlan holds the plan produced by /plan so /plan run can execute it.
	lastPlan *agent.Plan
	// notReady explains why the provider cannot be used yet, if it cannot.
	notReady error
	// streamedText records that prose was printed, so the next block starts on
	// a fresh line.
	streamedText bool
	// afterTool records that a tool ran since the last text, so the model's next
	// sentence is separated from the previous one.
	afterTool bool
	// activeSession is the session file this run keeps appending to.
	activeSession *session.Session
	// plugins and mcpServers hold the external tool providers.
	plugins    []*plugin.Plugin
	mcpServers []*mcp.Server

	// posture is the assembled security configuration for this run; every
	// component below is wired through it.
	posture *secure.Posture
	// legal records which Terms and Privacy versions the user accepted.
	legal *legal.Ledger
	// httpClient is the provider client wrapped by the network policy.
	httpClient *http.Client
	// auditSessionID correlates audit records for this run.
	auditSessionID string
}

// sessionID returns the identifier used for audit records.
func (r *REPL) sessionID() string {
	if r.auditSessionID != "" {
		return r.auditSessionID
	}
	return r.opts.SessionID
}

// New builds a REPL.
func New(opts Options) (*REPL, error) {
	theme := ui.ThemeByName(opts.Config.UI.Theme)
	if opts.Stdout != nil && !ui.SupportsColor(fileOf(opts.Stdout)) {
		theme = ui.MonoTheme()
	}
	r := &REPL{
		opts:     opts,
		cfg:      opts.Config,
		theme:    theme,
		pr:       ui.NewPrinter(opts.Stdout, opts.Stderr, theme),
		exitCh:   make(chan struct{}),
		journal:  journal.New(journalPath(opts.Workspace)),
		sessions: session.NewStore(paths.SessionsDir(), opts.Config.Sessions.Max),
		spinner: ui.NewSpinner(opts.Stdout, theme,
			opts.Config.UI.Spinner && term.IsTerminal(fileOf(opts.Stdout))),
	}
	if opts.Width > 0 {
		r.pr.Width = opts.Width
	}
	if err := r.build(); err != nil {
		return nil, err
	}
	r.agent = agent.New(agent.Options{
		Provider:    r.resolveProvider(),
		Tools:       r.tools,
		ToolCtx:     r.toolCtx,
		System:      r.systemPrompt(),
		MaxSteps:    opts.Config.Agent.MaxSteps,
		Mode:        modeFromLevel(opts.Config.Permissions.Level),
		Approver:    r,
		Log:         opts.Log,
		Temperature: opts.Config.Model.Temperature,
		MaxTokens:   opts.Config.Model.MaxTokens,
		OnEvent:     r.onEvent,
	})
	if r.opts.Provider == nil {
		if err := r.applyProvider(r.resolveProvider()); err != nil {
			r.notReady = err
			return r, nil
		}
	}
	return r, nil
}

// build assembles the project services.
func (r *REPL) build() error {
	ctx := context.Background()
	if err := r.buildSecurity(ctx); err != nil {
		return err
	}
	r.shell = shell.NewRunner()
	r.git = git.New(r.opts.Workspace)
	r.index = index.New(r.opts.Workspace)
	if err := r.index.Build(ctx); err != nil {
		return errs.Wrap(errs.KindInternal, "repl", err)
	}
	r.project = project.Detect(ctx, r.opts.Workspace)

	mem, err := memory.Load(r.opts.Workspace, r.cfg.Memory.MaxNotes)
	if err != nil {
		return err
	}
	r.memory = mem

	r.tools = tools.NewRegistry()
	r.tools.Register(tools.All()...)
	r.tools.Register(r.opts.ExtraTools...)

	limits := tools.DefaultLimits()
	limits.MaxOutputBytes = r.cfg.Agent.MaxToolOutput
	limits.CommandTimeoutSeconds = r.cfg.Permissions.TimeoutSeconds

	r.toolCtx = &tools.Context{
		Ctx:       ctx,
		Workspace: r.opts.Workspace,
		Limits:    limits,
		Policy: perm.New(perm.Config{
			Level:          r.permissionFor(r.cfg.Permissions.Level),
			Workspace:      r.opts.Workspace,
			AllowPaths:     r.cfg.Permissions.AllowPaths,
			DenyPaths:      r.cfg.Permissions.DenyPaths,
			DenyCommands:   r.cfg.Permissions.DenyCommands,
			AllowCommands:  r.cfg.Permissions.AllowCommands,
			AlwaysAskTools: r.cfg.Agent.AutoApproveTools,
		}),
		Shell:           r.shell,
		Git:             r.git,
		Index:           r.index,
		Project:         r.project,
		Journal:         r.journal,
		Log:             r.opts.Log,
		OnCommandOutput: r.onCommandOutput,
	}
	r.toolCtx.Ctx = context.Background()
	r.toolCtx.Gates = r.gates()
	if err := r.loadPlugins(ctx); err != nil {
		r.pr.Warn(errs.User(err))
	}
	if err := r.loadMCPServers(ctx); err != nil {
		r.pr.Warn(errs.User(err))
	}
	return nil
}

// loadPlugins installs every plugin found in the plugin directory and registers
// the tools it exposes.
func (r *REPL) loadPlugins(ctx context.Context) error {
	if !r.cfg.Plugins.Enabled {
		return nil
	}
	dirs, err := plugin.Discover(r.cfg.Plugins.Dir)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return nil
	}
	for _, dir := range dirs {
		p, err := plugin.Install(ctx, dir, r.opts.Log)
		if err != nil {
			r.pr.Warn(fmt.Sprintf("plugin %s: %s", filepath.Base(dir), errs.User(err)))
			continue
		}
		r.plugins = append(r.plugins, p)
		defs := p.Definitions(r.toolCtx)
		r.tools.Register(defs...)
		r.pr.Muted(fmt.Sprintf("plugin %s v%s: %d tool(s)", p.Manifest.Name, p.Manifest.Version, len(defs)))
	}
	return nil
}

// loadMCPServers connects the configured MCP servers.
func (r *REPL) loadMCPServers(ctx context.Context) error {
	if !r.cfg.MCP.Enabled {
		return nil
	}
	for _, s := range r.cfg.MCP.Servers {
		if !s.Enabled {
			continue
		}
		srv, err := mcp.Connect(ctx, mcp.Spec{
			Name: s.Name, Command: s.Command, Args: s.Args, Env: s.Env,
		}, r.opts.Log)
		if err != nil {
			r.pr.Warn(fmt.Sprintf("mcp %s: %s", s.Name, errs.User(err)))
			continue
		}
		r.mcpServers = append(r.mcpServers, srv)
		defs := srv.Definitions()
		r.tools.Register(defs...)
		r.pr.Muted(fmt.Sprintf("mcp %s: %d tool(s)", s.Name, len(defs)))
	}
	return nil
}

// resolveProvider returns the injected provider or builds one from config.
func (r *REPL) resolveProvider() llm.Provider {
	if r.opts.Provider != nil {
		return r.opts.Provider
	}
	return r.provider()
}

// provider builds the configured LLM provider.
func (r *REPL) provider() llm.Provider {
	p, err := llm.New(r.cfg.Model.Provider, r.providerOptions())
	if err != nil {
		// Fall back to the mock provider so the session still starts and the
		// user can read the error in context.
		r.pr.Failure(errs.User(err))
		return llm.NewMock(llm.Options{})
	}
	return p
}

// applyProvider validates the provider configuration early so the user finds
// out immediately instead of on the first request.
func (r *REPL) applyProvider(p llm.Provider) error {
	return llm.Validate(r.cfg.Model.Provider, llm.Options{
		APIKey:  r.cfg.APIKeyValue(),
		Model:   r.cfg.Model.Name,
		BaseURL: r.cfg.Model.BaseURL,
	})
}

// Ready reports whether the agent can talk to the provider.
func (r *REPL) Ready() bool { return r.notReady == nil }

// NotReadyReason returns the configuration problem blocking the provider, if any.
func (r *REPL) NotReadyReason() error { return r.notReady }

// systemPrompt builds the prompt for this session.
func (r *REPL) systemPrompt() string {
	builder := buildcontext.Builder{
		Config:     r.cfg,
		Project:    r.project,
		Index:      r.index,
		Memory:     r.memory,
		Workspace:  r.opts.Workspace,
		GitSummary: r.gitSummary(),
	}
	return buildcontext.WithToolGuide(builder.System())
}

// gitSummary renders the repository state for the prompt.
func (r *REPL) gitSummary() string {
	if !r.project.HasGit {
		return ""
	}
	files, err := r.git.Status(context.Background())
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Repository at %s, branch %s.", r.project.GitRoot, r.project.GitBranch)
	if len(files) == 0 {
		b.WriteString(" The working tree is clean.")
		return b.String()
	}
	b.WriteString(fmt.Sprintf(" %d uncommitted change(s):", len(files)))
	for i, f := range files {
		if i >= 10 {
			fmt.Fprintf(&b, "\n  … and %d more", len(files)-i)
			break
		}
		fmt.Fprintf(&b, "\n  %s %s", f.Label(), f.Path)
	}
	b.WriteString("\nThese changes may be the user's work in progress: read them before modifying the same files.")
	return b.String()
}

// SetMode changes the autonomy mode at runtime.
func (r *REPL) SetMode(m agent.Mode) { r.agent.SetMode(m) }

// Conversation exposes the message history (used by tests and /context).
func (r *REPL) Conversation() *agent.Conversation { return r.agent.Conversation() }

// Mode returns the current autonomy mode.
func (r *REPL) Mode() agent.Mode { return r.agent.Mode() }

// AppendSystem adds extra instructions to the system prompt for this session.
func (r *REPL) AppendSystem(text string) {
	r.agent.SetSystem(r.systemPrompt() + "\n\n" + strings.TrimSpace(text))
}

// Run executes the session.
func (r *REPL) Run(ctx context.Context) error {
	defer r.shutdown()
	r.banner()
	r.checkConsent()

	if strings.TrimSpace(r.opts.InitialPrompt) != "" {
		return r.turn(ctx, r.opts.InitialPrompt)
	}
	if r.opts.NonInteractive {
		return errs.Usage("no prompt given; pass one as an argument or run talon interactively")
	}

	r.setupReadline()
	for {
		if r.exiting {
			return nil
		}
		line, err := r.rl.ReadLine(r.prompt())
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.pr.Blank()
				return nil
			}
			if errors.Is(err, term.ErrInterrupt) {
				r.pr.Muted("cancelled")
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r.handleSlash(ctx, line) {
			continue
		}
		if err := r.turn(ctx, line); err != nil {
			r.reportTurnError(err)
		}
	}
}

// turn runs one user request end to end.
func (r *REPL) turn(ctx context.Context, input string) error {
	if r.notReady != nil {
		r.pr.Failure(errs.User(r.notReady))
		r.pr.Muted("configure a provider first: talon init, then set AI_API_KEY (or `talon models --offline` to pick one)")
		return nil
	}
	r.pendingDiffs = nil
	r.afterTool = false
	r.spinner.Start("thinking")
	if r.rl != nil && r.rl.History != nil {
		r.rl.History.Add(input)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.currentTurnCancel = cancel

	msg, err := r.agent.Run(streamCtx, input)
	r.spinner.Stop("")
	r.flushStream()
	if err != nil {
		return err
	}
	if r.cfg.Sessions.Autosave {
		r.saveSession(input)
	}
	if r.cfg.Context.AutoCompact && r.agent.Conversation().NeedsCompaction(r.cfg.Context.CompactAt) {
		r.pr.Muted("compacting the conversation to stay inside the context window…")
		if dropped, cerr := r.agent.Compact(streamCtx); cerr == nil && dropped > 0 {
			r.pr.Muted(fmt.Sprintf("compacted %d messages into a summary", dropped))
		}
	}
	_ = msg
	return nil
}

// reportTurnError prints an error without stopping the session.
func (r *REPL) reportTurnError(err error) {
	switch {
	case errs.IsKind(err, errs.KindCancelled):
		r.pr.Muted("interrupted")
	default:
		r.pr.Failure(errs.User(err))
	}
}

// onEvent renders agent progress.
func (r *REPL) onEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EventTurnStart:
		r.afterTool = false
		if r.streamedText {
			// Prose from the previous turn must not run into this one.
			r.pr.Blank()
			r.streamedText = false
		}
		r.spinner.Start("thinking")
	case agent.EventText:
		r.stopSpinnerForOutput()
		if r.stream == nil {
			r.stream = ui.NewMarkdownStream(r.opts.Stdout, r.theme, r.pr.Width)
		} else if r.afterTool {
			// A sentence that follows tool activity starts a new paragraph.
			_, _ = io.WriteString(r.opts.Stdout, "\n")
			r.streamedText = false
		}
		r.afterTool = false
		_, _ = r.stream.Write([]byte(ev.Text))
	case agent.EventReasoning:
		// Model reasoning is never printed: only the actions it leads to.
	case agent.EventToolStart:
		// Flush any prose the model has produced so far: the tool line takes the
		// screen next, and unflushed text would appear after it.
		r.flushStream()
		r.spinner.Update(formatToolStart(ev.Tool))
	case agent.EventToolApproval:
		if ev.Text != "" && strings.Contains(ev.Text, "denied") {
			r.pr.Warn(fmt.Sprintf("%s: %s", ev.Tool, ev.Text))
		}
	case agent.EventToolResult:
		r.flushStream()
		if ev.Err != nil {
			r.spinner.Stop(r.theme.Err(ui.GlyphFail + " tool failed"))
			break
		}
		res := ev.Result
		if res == nil {
			break
		}
		line := fmt.Sprintf("%s %s", ui.GlyphOK, res.Display)
		if res.Diff != nil {
			r.pendingDiffs = append(r.pendingDiffs, *res.Diff)
			if !strings.Contains(line, "+") {
				line += "  " + ui.PreviewStats(res.Diff.Stats)
			}
		}
		r.spinner.Stop(r.theme.Ok(line))
		r.afterTool = true
	case agent.EventNotice:
		r.spinner.Stop(r.theme.Warn(ui.GlyphWarn + " " + ev.Text))
	case agent.EventError:
		r.spinner.Stop("")
		r.pr.Failure(errs.User(ev.Err))
	case agent.EventTurnEnd:
		r.spinner.Stop("")
		r.flushStream()
	}
}

// flushStream renders the tail of a streamed answer.
func (r *REPL) flushStream() {
	if r.stream == nil {
		return
	}
	_ = r.stream.Flush()
	r.stream = nil
	r.streamedText = true
}

func formatToolStart(name string) string {
	return name
}

// stopSpinnerForOutput clears the spinner before the model writes text.
func (r *REPL) stopSpinnerForOutput() {
	if r.spinner != nil {
		r.spinner.Stop("")
	}
}

// onCommandOutput streams shell output while the spinner is running.
func (r *REPL) onCommandOutput(stream, chunk string) {
	if !r.cfg.UI.Stream {
		return
	}
	r.spinner.Stop("")
	r.pr.Status(ui.GlyphBullet2, r.theme.Muted, "", strings.TrimRight(chunk, "\n"))
	r.spinner.Start("working")
}

// banner prints the session header.
func (r *REPL) banner() {
	r.pr.Line(r.theme.Strong("Talon"))
	r.pr.Muted(fmt.Sprintf("agentic coding CLI · %s · %s", r.cfg.EffectiveModel(), r.opts.Workspace))
	desc := r.describeProject()
	if desc != "" {
		r.pr.Muted(desc)
	}
	r.pr.Muted(fmt.Sprintf("tools: %d · permissions: %s · /help for commands",
		r.tools.Len(), r.cfg.Permissions.Level))
	r.pr.Blank()
}

func (r *REPL) describeProject() string {
	if r.project.Primary == "" {
		return ""
	}
	s := fmt.Sprintf("project: %s", r.project.Primary)
	if r.project.FileCount > 0 {
		s += fmt.Sprintf(", %d files", r.project.FileCount)
	}
	if r.project.HasGit {
		s += fmt.Sprintf(", git %s", r.project.GitBranch)
	}
	return s
}

// shutdown persists state and restores the terminal.
func (r *REPL) shutdown() {
	r.spinner.Stop("")
	if r.rl != nil && r.rl.History != nil {
		_ = r.rl.History.Save()
	}
	for _, srv := range r.mcpServers {
		_ = srv.Stop()
	}
	for _, p := range r.plugins {
		_ = p.Stop()
	}
}

// Plugins returns the loaded plugins (used by /plugins).
func (r *REPL) Plugins() []*plugin.Plugin { return r.plugins }

// MCPServers returns the connected MCP servers.
func (r *REPL) MCPServers() []*mcp.Server { return r.mcpServers }

// saveSession persists the conversation. One file is kept per run: each turn
// rewrites it with the full history, so resuming later restores everything.
func (r *REPL) saveSession(prompt string) {
	msgs := r.agent.Conversation().Messages()
	if len(msgs) < 2 {
		return
	}
	if r.activeSession == nil {
		id := os.Getenv("TALON_SESSION_ID")
		if id == "" {
			id = session.NewID()
		}
		r.activeSession = &session.Session{ID: id}
	}
	sess := session.FromMessages(r.activeSession.ID, sessionName(prompt), r.opts.Workspace,
		r.cfg.EffectiveModel(), msgs)
	sess.Created = r.activeSession.Created
	sess.Tokens += r.agent.LastUsage().TotalTokens
	var changes []string
	for _, e := range r.journal.Entries() {
		changes = append(changes, e.Path)
	}
	sess.Changes = uniqueStrings(changes)
	if err := r.sessions.Save(sess); err != nil {
		r.opts.Log.Debugf("session save: %v", err)
		return
	}
	r.activeSession = sess
}

func sessionName(prompt string) string {
	name := strings.TrimSpace(strings.ReplaceAll(prompt, "\n", " "))
	if len(name) > 50 {
		name = name[:50] + "…"
	}
	return name
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// setupReadline configures the line editor and its completer.
func (r *REPL) setupReadline() {
	hist := term.NewHistory(paths.HistoryPath(), 1000)
	r.rl = term.NewReadline(r.opts.Stdin, r.opts.Stdout, hist)
	r.rl.Completer = term.CompleterFunc(r.complete)
	r.rl.Suggest = func(prefix string) []string { return r.suggest(prefix) }
	r.rl.Prompt = term.DefaultAskPrompt()
}

// prompt renders the input prompt, including the current mode.
func (r *REPL) prompt() string {
	mode := r.agent.Mode()
	p := term.DefaultAskPrompt()
	switch mode {
	case agent.ModePlan:
		p.Hint = "plan"
		p.HintStyle = r.theme.Warning
	case agent.ModeAuto:
		p.Hint = "auto"
		p.HintStyle = r.theme.Accent
	}
	return p.Render()
}

// complete implements tab completion for slash commands and paths.
func (r *REPL) complete(line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 || (len(fields) == 1 && strings.HasPrefix(line, "/")) {
		return prefixedCommands("/")
	}
	if strings.HasPrefix(line, "/") {
		return nil
	}
	// Complete the last path-looking argument, relative to the project.
	last := fields[len(fields)-1]
	dir, prefix := splitPath(last)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.opts.Workspace, filepath.FromSlash(dir))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		full := dir + name
		if e.IsDir() {
			full += "/"
		}
		out = append(out, full)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// suggest returns hint lines shown under the prompt.
func (r *REPL) suggest(prefix string) []string {
	if strings.HasPrefix(prefix, "/") {
		var out []string
		for _, name := range slashCommandNames() {
			if strings.Contains(name, strings.TrimPrefix(prefix, "/")) {
				out = append(out, "/"+name+" — "+slashDescriptions[name])
			}
		}
		return out
	}
	if len(prefix) > 24 || strings.ContainsAny(prefix, "\n ") {
		return nil
	}
	return nil
}

func splitPath(p string) (dir, prefix string) {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return "./", p
	}
	if idx == 0 {
		return "/", p[1:]
	}
	return p[:idx+1], p[idx+1:]
}

// fileOf extracts the *os.File behind a writer when possible.
func fileOf(w io.Writer) *os.File {
	if f, ok := w.(*os.File); ok {
		return f
	}
	return nil
}

// modeFromLevel maps a permission level to the default agent mode.
func modeFromLevel(level string) agent.Mode {
	switch perm.Level(level) {
	case perm.FullAccess:
		return agent.ModeAuto
	case perm.ReadOnly, perm.Safe, perm.Confirm:
		return agent.ModeAsk
	default:
		return agent.ModeAsk
	}
}

// journalPath returns the undo journal location for a workspace.
func journalPath(workspace string) string {
	return filepath.Join(paths.Cache(), "journal", safeName(workspace)+".jsonl")
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
