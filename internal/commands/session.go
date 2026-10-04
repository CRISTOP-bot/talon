package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/CRISTOP-bot/talon/internal/term"
	"github.com/CRISTOP-bot/talon/internal/ui"

	"github.com/CRISTOP-bot/talon/internal/agent"
	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/logger"
	"github.com/CRISTOP-bot/talon/internal/repl"
)

// sessionFlags are the flags accepted in front of a prompt.
type sessionFlags struct {
	yes        bool
	model      string
	plan       bool
	nonInter   bool
	appendText string
	noIndex    bool
	noColor    bool
	debug      bool
	privacy    bool
	noSandbox  bool
	accept     bool
	auditLevel string
	noTUI      bool
}

// parseSessionFlags parses the arguments that precede the prompt.
func parseSessionFlags(args []string) (*sessionFlags, []string, error) {
	f := &sessionFlags{}
	fs := flag.NewFlagSet("talon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&f.yes, "yes", false, "approve every tool call without asking")
	fs.BoolVar(&f.yes, "y", false, "alias for --yes")
	fs.StringVar(&f.model, "model", "", "override the model as provider/name")
	fs.BoolVar(&f.plan, "plan", false, "start in plan mode")
	fs.BoolVar(&f.nonInter, "non-interactive", false, "run one prompt and exit")
	fs.StringVar(&f.appendText, "append-system", "", "extra system instructions")
	fs.BoolVar(&f.noIndex, "no-index", false, "skip indexing the project at startup")
	fs.BoolVar(&f.noColor, "no-color", false, "disable colours")
	fs.BoolVar(&f.debug, "debug", false, "write a debug log")
	fs.BoolVar(&f.privacy, "privacy", false, "keep nothing on disk: no sessions, logs or audit trail")
	fs.BoolVar(&f.noSandbox, "no-sandbox", false, "run commands without kernel isolation (less safe)")
	fs.BoolVar(&f.accept, "accept-terms", false, "record acceptance of the current Terms version")
	fs.StringVar(&f.auditLevel, "audit-level", "", "audit detail: minimal, normal, verbose or off")
	fs.BoolVar(&f.noTUI, "no-tui", false, "use the line editor instead of the full-screen interface")
	if err := fs.Parse(args); err != nil {
		return nil, nil, errs.Usage("%v", err)
	}
	return f, fs.Args(), nil
}

// RunSession is the entry point for `talon` with no subcommand and for
// `talon "prompt"`.
func RunSession(a *cli.App, args []string) int {
	f, positional, err := parseSessionFlags(args)
	if err != nil {
		fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
		return 2
	}

	root, err := a.Globals().WorkingDir()
	if err != nil {
		fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
		return 2
	}

	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
		return 2
	}
	if a.Globals().Debug || f.debug {
		cfg.Logging.Level = "debug"
	}
	if f.noColor {
		cfg.UI.Theme = "mono"
	}
	if a.Globals().Verbose && cfg.Logging.Level == "off" {
		cfg.Logging.Level = "info"
	}
	if a.Globals().NoColor {
		cfg.UI.Theme = "mono"
	}
	if f.model != "" {
		applyModelOverride(cfg, f.model)
	}
	if f.plan {
		cfg.Permissions.Level = string("confirm")
	}
	if f.privacy {
		cfg.Security.PrivacyMode = true
		// Privacy mode is incompatible with unattended approval: the user must
		// see every action.
		f.yes = false
		cfg.Permissions.Level = "confirm"
	}
	if f.noSandbox {
		cfg.Security.Sandbox = false
	}
	if f.auditLevel != "" {
		cfg.Security.AuditLevel = f.auditLevel
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
		return 2
	}
	logFile := a.Globals().LogFile
	if f.privacy || cfg.Security.PrivacyMode {
		// Privacy mode means nothing is written to disk, including the log.
		cfg.Logging.File = ""
		cfg.Logging.Level = "off"
		logFile = ""
	} else if logFile == "" {
		logFile = cfg.Logging.File
	}
	log := logger.New(logger.Options{
		Level:  logger.ParseLevel(cfg.Logging.Level),
		File:   logFile,
		Stderr: a.Stderr,
		Redact: true,
	})
	defer log.Close()
	logger.SetDefault(log)
	if key := cfg.APIKeyValue(); key != "" {
		logger.RegisterSecret(key)
	}
	log.Infof("talon starting in %s with %s", root, cfg.EffectiveModel())

	width := terminalWidth()

	prompt := strings.Join(positional, " ")
	oneShot := prompt != "" || f.nonInter

	if f.plan {
		log.Debugf("plan mode requested on the command line")
	}

	if f.accept && (f.privacy || cfg.Security.PrivacyMode) {
		// Consent is recorded on disk; a private run must not write anything, so
		// accepting here would be a lie about what the run keeps.
		fmt.Fprintln(a.Stderr,
			"error: --accept-terms cannot be combined with --privacy: acceptance is stored on disk. "+
				"Run talon terms accept once without --privacy, then use --privacy.")
		return 2
	}
	if f.accept {
		if err := acceptTermsFlag(); err != nil {
			fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
			return 1
		}
		log.Infof("terms acceptance recorded from the command line")
	}

	r, err := repl.New(repl.Options{
		Config:         cfg,
		Workspace:      root,
		PrivacyMode:    f.privacy || cfg.Security.PrivacyMode,
		Stdout:         a.Stdout,
		Stderr:         a.Stderr,
		Stdin:          os.Stdin,
		Log:            log,
		NonInteractive: oneShot,
		Width:          width,
		AutoApprove:    f.yes,
		InitialPrompt:  prompt,
		SessionID:      sessionIDFor(root, prompt),
		NoTUI:          f.noTUI,
	})
	if err != nil {
		fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
		return 1
	}
	if f.plan {
		r.SetMode(agent.ModePlan)
	}
	if f.appendText != "" {
		r.AppendSystem(f.appendText)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(a.Stderr, "error: "+errs.User(err))
		return exitCodeFor(err)
	}
	log.Infof("talon finished")
	return 0
}

// sessionIDFor derives a stable identifier for one run so audit records can be
// correlated. It contains no user data beyond the workspace path hash.
func sessionIDFor(root, prompt string) string {
	sum := sha256.Sum256([]byte(root + "\x00" + prompt))
	return hex.EncodeToString(sum[:8])
}

// terminalWidth returns the terminal width for the printer, capped to a
// readable range.
func terminalWidth() int {
	w, _, err := term.Size(os.Stdin)
	if err != nil || w < 40 {
		return 100
	}
	if w > 120 {
		return 120
	}
	return w
}

// applyModelOverride parses "provider/name" or "name" into the configuration.
func applyModelOverride(cfg *config.Config, spec string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return
	}
	if i := strings.Index(spec, "/"); i > 0 {
		cfg.Model.Provider = spec[:i]
		cfg.Model.Name = spec[i+1:]
		return
	}
	cfg.Model.Name = spec
}

// exitCodeFor maps an error to a process exit code.
func exitCodeFor(err error) int {
	switch errs.KindOf(err) {
	case errs.KindUsage:
		return 2
	case errs.KindCancelled:
		return 130
	default:
		return 1
	}
}

// initCommand writes a starter configuration file.
func initCommand() *cli.Command {
	var force bool
	var provider, model string
	return &cli.Command{
		Name:    "init",
		Summary: "create a starter configuration file",
		Detailed: strings.TrimSpace(`
Writes ~/.config/talon/config.toml with the selected provider and model.

The API key is never written to disk: export it (AI_API_KEY) or store it in
your shell profile. A key placed in the config file is stored with mode 0600.
`),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&force, "force", false, "overwrite an existing configuration file")
			fs.StringVar(&provider, "provider", "", "provider to configure (openai, anthropic, gemini, openrouter, ollama, llamacpp, custom)")
			fs.StringVar(&model, "model", "", "model id to configure")
		},
		Run: func(c *cli.Context, args []string) error {
			root, err := c.App.Globals().WorkingDir()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(cfg.UserFile); statErr == nil && !force {
				e := errs.New(errs.KindUsage, "config",
					fmt.Sprintf("%s already exists (use --force to overwrite)", cfg.UserFile))
				return e
			}
			if provider != "" {
				cfg.SetIn("model.provider", quoteTOML(provider), "user")
			}
			if model != "" {
				cfg.SetIn("model.name", quoteTOML(model), "user")
			}
			if err := cfg.SetIn("model.api_key_env", quoteTOML("AI_API_KEY"), "user"); err != nil {
				return err
			}
			if err := cfg.SaveUser(); err != nil {
				return err
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName("default"))
			pr.Success("wrote " + cfg.UserFile)
			pr.Muted("next: export AI_API_KEY=… and run `talon` in your project")
			if cfg.APIKeyValue() == "" {
				pr.Warn("no API key found in the environment yet")
			}
			return nil
		},
	}
}

func quoteTOML(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
