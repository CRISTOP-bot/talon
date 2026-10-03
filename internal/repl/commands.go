package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/agent"
	buildcontext "github.com/CRISTOP-bot/talon/internal/context"
	"github.com/CRISTOP-bot/talon/internal/diff"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/index"
	"github.com/CRISTOP-bot/talon/internal/journal"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/session"
)

// slashDescriptions documents each command for /help and completion.
var slashDescriptions = map[string]string{
	"help":        "show this help",
	"exit":        "leave Talon (or /quit)",
	"clear":       "forget the conversation and start fresh",
	"plan":        "switch to plan mode: propose a plan before changing anything",
	"mode":        "show or set the autonomy mode (ask, auto)",
	"model":       "show the current model",
	"models":      "list the models the provider offers",
	"context":     "show the context that is sent to the model",
	"tools":       "list the available tools",
	"permissions": "show or change the permission level",
	"session":     "list, save, load or delete sessions",
	"diff":        "show the changes made in this session",
	"undo":        "revert the last change",
	"redo":        "reapply the last reverted change",
	"compact":     "summarise the conversation to free context",
	"memory":      "list, add or forget remembered notes",
	"index":       "rebuild the project index",
	"git":         "show the git status and the current diff",
	"security":    "show the security controls in force",
	"sandbox":     "show the kernel sandbox status",
	"terms":       "show, diff or accept the Terms",
	"privacy":     "show the Privacy Notice and the data Talon keeps",
	"audit":       "show the local audit log",
	"data":        "list or clear the data Talon stores",
}

// prefixedCommands returns the command names with a prefix, e.g. "/plan".
func prefixedCommands(prefix string) []string {
	names := slashCommandNames()
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, prefix+n)
	}
	return out
}

// slashCommandNames returns the command names, sorted.
func slashCommandNames() []string {
	out := make([]string, 0, len(slashDescriptions))
	for name := range slashDescriptions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// handleSlash processes a slash command. It returns true when the line was a
// command and should not be sent to the model.
func (r *REPL) handleSlash(ctx context.Context, line string) bool {
	if !strings.HasPrefix(line, "/") {
		return false
	}
	fields := strings.Fields(line)
	name := strings.TrimPrefix(fields[0], "/")
	args := fields[1:]
	switch name {
	case "help", "?":
		r.printHelp()
	case "exit", "quit", "q":
		r.exiting = true
		r.pr.Muted("bye")
	case "clear":
		r.agent.Reset()
		r.journal.Clear()
		r.pendingDiffs = nil
		r.pr.Success("conversation cleared")
	case "plan":
		r.cmdPlan(ctx, args)
	case "mode":
		r.cmdMode(args)
	case "model":
		r.cmdModel(args)
	case "models":
		r.cmdModels(ctx)
	case "context":
		r.cmdContext()
	case "tools":
		r.cmdTools(args)
	case "permissions":
		r.cmdPermissions(args)
	case "session":
		r.cmdSessions(args)
	case "diff":
		r.cmdDiff()
	case "undo":
		r.cmdUndo()
	case "redo":
		r.cmdRedo()
	case "compact":
		r.cmdCompact(ctx)
	case "memory":
		r.cmdMemory(args)
	case "index":
		r.cmdIndex()
	case "git":
		r.cmdGit(ctx)
	case "security":
		r.cmdSecurity(args)
	case "sandbox":
		r.cmdSandbox()
	case "terms":
		r.cmdTerms(args)
	case "privacy":
		r.cmdPrivacy(args)
	case "audit":
		r.cmdAudit(args)
	case "data":
		r.cmdData(args)
	default:
		r.pr.Failure(fmt.Sprintf("unknown command /%s — try /help", name))
	}
	return true
}

func (r *REPL) printHelp() {
	r.pr.Blank()
	r.pr.Line(r.theme.Strong("Commands"))
	names := slashCommandNames()
	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}
	for _, n := range names {
		r.pr.Printf("  %-*s  %s\n", width, "/"+n, r.theme.Faint(slashDescriptions[n]))
	}
	r.pr.Blank()
	r.pr.Muted("Anything else is sent to the model. Ctrl+C interrupts, Ctrl+D exits, ↑/↓ recall history,")
	r.pr.Muted("Tab completes commands and paths, Ctrl+R searches history.")
	r.pr.Blank()
}

func (r *REPL) cmdMode(args []string) {
	if len(args) == 0 {
		r.pr.Printf("mode: %s  (permissions: %s)\n", r.agent.Mode(), r.cfg.Permissions.Level)
		return
	}
	switch agent.Mode(strings.ToLower(args[0])) {
	case agent.ModeAsk:
		r.agent.SetMode(agent.ModeAsk)
	case agent.ModeAuto:
		r.agent.SetMode(agent.ModeAuto)
	case agent.ModePlan:
		r.agent.SetMode(agent.ModePlan)
		r.pr.Muted("plan mode is for a single request; use /plan <request> instead")
		return
	default:
		r.pr.Failure("unknown mode: " + args[0] + " (ask, auto)")
		return
	}
	r.pr.Success("mode: " + string(r.agent.Mode()))
}

func (r *REPL) cmdPlan(ctx context.Context, args []string) {
	if len(args) > 0 && (args[0] == "run" || args[0] == "execute") {
		r.cmdPlanRun(ctx, args[1:])
		return
	}
	if len(args) == 0 {
		r.agent.SetMode(agent.ModePlan)
		r.pr.Muted("plan mode on: the next request will propose a plan without changing anything")
		return
	}
	r.agent.SetMode(agent.ModePlan)
	r.pendingDiffs = nil
	r.spinner.Start("planning")
	plan, err := r.agent.PlanMode(ctx, strings.Join(args, " "))
	r.spinner.Stop("")
	r.agent.SetMode(modeFromLevel(r.cfg.Permissions.Level))
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	r.lastPlan = plan
	r.pr.Blank()
	r.printPlan(plan)
	r.pr.Blank()
	r.pr.Muted("Run it with /plan run once you are happy with it.")
}

func (r *REPL) printPlan(plan *agent.Plan) {
	if plan == nil || len(plan.Steps) == 0 {
		r.pr.Warn("the model did not produce a plan")
		return
	}
	if plan.Summary != "" {
		r.pr.Line(r.pr.WrapText(plan.Summary))
		r.pr.Blank()
	}
	r.pr.Line(r.theme.Strong("Plan"))
	for _, s := range plan.Steps {
		r.pr.Status(r.theme.Paint(r.theme.Primary, fmt.Sprintf("%d.", s.Number)), "",
			"", r.pr.WrapText(s.Text))
	}
}

// cmdPlanRun executes a plan after the user approves it.
func (r *REPL) cmdPlanRun(ctx context.Context, args []string) {
	lastPlan := r.lastPlan
	if lastPlan == nil {
		r.pr.Failure("no plan to run; ask for one with /plan <request>")
		return
	}
	r.agent.SetMode(modeFromLevel(r.cfg.Permissions.Level))
	err := r.agent.ExecutePlan(ctx, lastPlan, agent.ExecuteOptions{
		BeforeStep: func(step agent.PlanStep, index int) error {
			r.pr.Info(fmt.Sprintf("step %d/%d: %s", index, len(lastPlan.Steps), step.Text))
			return nil
		},
		AfterStep: func(step agent.PlanStep, index int, reply string) {
			r.pr.Success(fmt.Sprintf("step %d done: %s", index, oneline(reply, 160)))
		},
	})
	if err != nil {
		r.pr.Failure(errs.User(err))
	}
}

func (r *REPL) cmdModel(args []string) {
	if len(args) == 0 {
		r.pr.Printf("provider: %s\nmodel:    %s\nbase URL: %s\n", r.cfg.Model.Provider,
			orUnknown(r.cfg.Model.Name), orUnknown(r.cfg.Model.BaseURL))
		r.pr.Muted("change it with `talon config set model.name <id>` or /model <provider> <model>")
		return
	}
	if len(args) == 1 {
		r.cfg.Model.Provider = args[0]
	} else {
		r.cfg.Model.Provider, r.cfg.Model.Name = args[0], args[1]
	}
	if err := llm.Validate(r.cfg.Model.Provider, llm.Options{
		APIKey: r.cfg.APIKeyValue(), Model: r.cfg.Model.Name, BaseURL: r.cfg.Model.BaseURL,
	}); err != nil {
		r.pr.Warn(errs.User(err))
	}
	r.agent.SetMode(modeFromLevel(r.cfg.Permissions.Level))
	r.pr.Success("model: " + r.cfg.EffectiveModel() + " (applies to the next request)")
}

func (r *REPL) cmdModels(ctx context.Context) {
	provider := r.cfg.Model.Provider
	catalog := llm.Catalog(provider)
	localOnly := llm.IsLocalProvider(provider)

	r.spinner.Start("listing models")
	models, err := r.resolveProvider().ListModels(ctx)
	r.spinner.Stop("")
	if err != nil {
		r.pr.Warn("could not reach the provider: " + errs.User(err))
		r.pr.Muted("showing the built-in defaults instead")
	} else {
		catalog = models
	}
	if len(catalog) == 0 {
		r.pr.Failure("no models found for provider " + provider)
		return
	}
	llm.SortModels(catalog)
	rows := make([][]string, 0, len(catalog))
	for _, m := range catalog {
		marker := " "
		if m.ID == r.cfg.Model.Name {
			marker = "*"
		}
		window := ""
		if m.ContextWindow > 0 {
			window = fmt.Sprintf("%d", m.ContextWindow)
		}
		rows = append(rows, []string{marker, m.DisplayName, m.ID, window})
		if localOnly {
			rows[len(rows)-1][3] = "local"
		}
	}
	r.pr.Table([]string{"", "name", "id", "context"}, rows)
	r.pr.Muted("set one with: talon config set model.name <id>")
}

func (r *REPL) cmdContext() {
	conv := r.agent.Conversation()
	pairs := [][2]string{
		{"provider", r.cfg.Model.Provider},
		{"model", orUnknown(r.cfg.Model.Name)},
		{"messages", fmt.Sprintf("%d", conv.Len())},
		{"approx tokens", fmt.Sprintf("%d / %d", conv.ApproxTokens(), conv.MaxTokens)},
		{"indexed files", fmt.Sprintf("%d", r.index.Len())},
		{"memory notes", fmt.Sprintf("%d", r.memory.Len())},
		{"tools", fmt.Sprintf("%d", r.tools.Len())},
		{"max steps", fmt.Sprintf("%d", r.cfg.Agent.MaxSteps)},
	}
	r.pr.KV(pairs)
	if conv.Summary() != "" {
		r.pr.Muted("conversation summary: " + oneline(conv.Summary(), 200))
	}
	if r.opts.Log != nil && r.opts.Log.Level() >= 0 {
		r.pr.Muted("the full system prompt is written to the log at debug level")
	}
	if r.opts.Log != nil {
		r.pr.Muted("run with --debug to see it: " + r.opts.Log.Path())
	}
	r.pr.Muted("")
	r.pr.Muted("system prompt:")
	r.pr.Line(r.pr.WrapText(r.systemPrompt()))
}

func (r *REPL) cmdTools(args []string) {
	if len(args) > 0 && args[0] == "suggest" {
		query := strings.Join(args[1:], " ")
		if strings.TrimSpace(query) == "" {
			r.pr.Failure("usage: /tools suggest <what you want to do>")
			return
		}
		picked := buildcontext.Suggest(query, r.tools.All(), 6)
		if len(picked) == 0 {
			r.pr.Muted("nothing matched")
			return
		}
		r.pr.Muted("tools that fit this request:")
		for _, d := range picked {
			r.pr.Status(r.theme.Paint(r.theme.Primary, "•"), "", d.Name, firstSentence(d.Description))
		}
		return
	}
	rows := make([][]string, 0, r.tools.Len())
	for _, d := range r.tools.All() {
		rows = append(rows, []string{d.Name, d.Risk.String(), d.Source, firstSentence(d.Description)})
	}
	r.pr.Table([]string{"tool", "risk", "source", "description"}, rows)
	r.pr.Muted("try /tools suggest <request>")
}

func (r *REPL) cmdPermissions(args []string) {
	if len(args) == 0 {
		r.pr.Line(r.pr.WrapText(r.toolCtx.Policy.Summarize()))
		r.pr.Muted("levels: read-only, safe, confirm, full-access")
		r.pr.Muted("example: /permissions safe   (or full-access for unattended edits)")
		return
	}
	level := perm.Level(strings.ToLower(args[0]))
	switch level {
	case perm.ReadOnly, perm.Safe, perm.Confirm, perm.FullAccess:
	default:
		r.pr.Failure("unknown level " + args[0] + " (read-only, safe, confirm, full-access)")
		return
	}
	r.toolCtx.Policy.SetLevel(level)
	r.cfg.Permissions.Level = string(level)
	r.agent.SetMode(modeFromLevel(string(level)))
	r.pr.Success("permissions: " + string(level))
	r.pr.Muted("this change lasts for the session; make it permanent with `talon config set permissions.level " + string(level) + "`")
}

func (r *REPL) cmdSessions(args []string) {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	switch sub {
	case "list", "":
		all, err := r.sessions.List(r.opts.Workspace)
		if err != nil {
			r.pr.Failure(errs.User(err))
			return
		}
		if len(all) == 0 {
			r.pr.Muted("no saved sessions for this project")
			return
		}
		rows := make([][]string, 0, len(all))
		for _, s := range all {
			rows = append(rows, []string{
				s.ID, s.Created.Local().Format("2006-01-02 15:04"),
				fmt.Sprintf("%d", s.MessageCount()), oneline(s.Summary, 60),
			})
		}
		r.pr.Table([]string{"id", "created", "turns", "summary"}, rows)
		r.pr.Muted("load one with /session load <id>")
	case "load", "resume":
		if len(rest) == 0 {
			r.pr.Failure("usage: /session load <id>")
			return
		}
		sess, err := r.sessions.Load(rest[0])
		if err != nil {
			r.pr.Failure(errs.User(err))
			return
		}
		r.loadSession(sess)
	case "save":
		name := "manual"
		if len(rest) > 0 {
			name = strings.Join(rest, " ")
		}
		id := session.SanitizeID(name)
		if id == "" {
			id = session.NewID()
		}
		sess := session.FromMessages(id, name, r.opts.Workspace, r.cfg.EffectiveModel(),
			r.agent.Conversation().Messages())
		if err := r.sessions.Save(sess); err != nil {
			r.pr.Failure(errs.User(err))
			return
		}
		r.pr.Success("saved as " + sess.ID)
	case "delete":
		if len(rest) == 0 {
			r.pr.Failure("usage: /session delete <id>")
			return
		}
		if err := r.sessions.Delete(rest[0]); err != nil {
			r.pr.Failure(errs.User(err))
			return
		}
		r.pr.Success("deleted " + rest[0])
	default:
		r.pr.Failure("unknown subcommand " + sub + " (list, load, save, delete)")
	}
}

func (r *REPL) loadSession(sess *session.Session) {
	r.agent.Reset()
	for _, m := range sess.Messages() {
		r.agent.AddMessage(m)
	}
	r.pr.Success(fmt.Sprintf("resumed %s (%d messages)", sess.ID, len(sess.Entries)))
	if sess.Model != "" && sess.Model != r.cfg.EffectiveModel() {
		r.pr.Muted("note: that session used " + sess.Model)
	}
}

func (r *REPL) cmdDiff() {
	if len(r.pendingDiffs) == 0 {
		r.pr.Muted("no changes in this session")
		return
	}
	r.pr.RenderDiff(r.opts.Stdout, r.pendingDiffs, r.cfg.Agent.MaxSteps)
}

func (r *REPL) cmdUndo() {
	entry, ok := r.journal.Undo()
	if !ok {
		r.pr.Muted("nothing to undo")
		return
	}
	if err := r.restore(entry, false); err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	r.pr.Success("reverted: " + entry.Description())
}

func (r *REPL) cmdRedo() {
	entry, ok := r.journal.Redo()
	if !ok {
		r.pr.Muted("nothing to redo")
		return
	}
	if err := r.restore(entry, true); err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	r.pr.Success("reapplied: " + entry.Description())
}

// restore writes a journal entry's content back to disk. Undo and redo share
// it: undoing restores Before, redoing restores After.
func (r *REPL) restore(entry journal.Entry, forward bool) error {
	abs, err := r.toolCtx.Resolve(entry.Path)
	if err != nil {
		return err
	}
	content := entry.Before
	if forward {
		content = entry.After
	}
	if content == "" {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return err
	}
	return nil
}

func (r *REPL) cmdCompact(ctx context.Context) {
	dropped, err := r.agent.Compact(ctx)
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	if dropped == 0 {
		r.pr.Muted("nothing to compact yet")
		return
	}
	r.pr.Success(fmt.Sprintf("compacted %d messages (%d remain, ~%d tokens)",
		dropped, r.agent.Conversation().Len(), r.agent.Conversation().ApproxTokens()))
}

func (r *REPL) cmdMemory(args []string) {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	switch sub {
	case "list", "":
		notes := r.memory.Notes()
		if len(notes) == 0 {
			r.pr.Muted("no notes yet; add one with /memory add <topic> <text>")
			return
		}
		rows := make([][]string, 0, len(notes))
		for _, n := range notes {
			rows = append(rows, []string{n.Topic, strings.Join(n.Tags, ","), oneline(n.Body, 70)})
		}
		r.pr.Table([]string{"topic", "tags", "note"}, rows)
	case "add":
		if len(rest) < 2 {
			r.pr.Failure("usage: /memory add <topic> <text>")
			return
		}
		if err := r.memory.Remember(rest[0], strings.Join(rest[1:], " ")); err != nil {
			r.pr.Failure(errs.User(err))
			return
		}
		r.pr.Success("remembered: " + rest[0])
	case "forget":
		if len(rest) == 0 {
			r.pr.Failure("usage: /memory forget <topic>")
			return
		}
		if err := r.memory.Forget(rest[0]); err != nil {
			r.pr.Failure(errs.User(err))
			return
		}
		r.pr.Success("forgot: " + rest[0])
	default:
		r.pr.Failure("unknown subcommand " + sub + " (list, add, forget)")
	}
}

func (r *REPL) cmdIndex() {
	r.spinner.Start("indexing the project")
	start := index.New(r.opts.Workspace)
	err := start.Build(context.Background())
	r.spinner.Stop("")
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	r.index = start
	r.toolCtx.Index = start
	stats := start.Stats()
	r.pr.Success(fmt.Sprintf("indexed %d files, %d symbols, %d bytes",
		stats.Files, stats.Symbols, stats.Bytes))
}

func (r *REPL) cmdGit(ctx context.Context) {
	if !r.project.HasGit {
		r.pr.Muted("this project is not a git repository")
		return
	}
	files, err := r.git.Status(ctx)
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	branch, _ := r.git.CurrentBranch(ctx)
	r.pr.Printf("branch %s · %d change(s)\n", branch, len(files))
	for _, f := range files {
		r.pr.Status(r.theme.Paint(r.theme.Primary, "•"), "", f.Label(), f.Path)
	}
	patch, err := r.git.Diff(ctx, false)
	if err != nil {
		r.pr.Failure(errs.User(err))
		return
	}
	if strings.TrimSpace(patch) == "" {
		r.pr.Muted("no unstaged changes")
		return
	}
	r.pr.Blank()
	r.pr.RenderDiff(r.opts.Stdout, diff.ParseUnified(patch), 3)
}

func oneline(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return oneline(s, 90)
}

func orUnknown(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

// planRunner dispatches "/plan run", which executes the last produced plan.
var _ = strings.TrimSpace
