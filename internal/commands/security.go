package commands

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/audit"
	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/legal"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/privacy"
	"github.com/CRISTOP-bot/talon/internal/sandbox"
	"github.com/CRISTOP-bot/talon/internal/secure"
)

const termsHelp = `
Shows the Terms of Use and records your acceptance.

Subcommands:
  current              print the Terms in this build (default)
  history              list the acceptances recorded on this machine
  diff [version]       summarise what changed against an earlier version
  accept               record acceptance of the current version

Acceptance is stored locally in a consent ledger; it is never sent anywhere and
it is not a subscription. Only material changes require new acceptance.
`

func termsCommand() *cli.Command {
	return &cli.Command{
		Name:     "terms",
		Summary:  "show the Terms of Use and record acceptance",
		Detailed: strings.TrimSpace(termsHelp),
		Run: func(c *cli.Context, args []string) error {
			sub := "current"
			if len(args) > 0 {
				sub = args[0]
			}
			doc, err := legal.CurrentTerms()
			if err != nil {
				return err
			}
			out := c.App.Stdout
			switch sub {
			case "current":
				fmt.Fprintf(out, "Talon Terms of Use %s (effective %s)\n\n", doc.Version, doc.EffectiveAt)
				fmt.Fprintln(out, strings.TrimRight(doc.Body, "\n"))
				status, serr := consentStatus(legal.KindTerms)
				if serr == nil && status.NeedsConsent {
					fmt.Fprintf(out, "\nThese Terms changed materially. Run: talon terms accept\n")
				}
				return nil
			case "history":
				ledger, lerr := legal.OpenLedger(privacy.LegalPath())
				if lerr != nil {
					return lerr
				}
				history := ledger.History(legal.KindTerms)
				if len(history) == 0 {
					fmt.Fprintln(out, "no acceptance recorded on this machine")
					return nil
				}
				for _, a := range history {
					fmt.Fprintf(out, "%s  %s  %s\n", a.Version,
						a.AcceptedAt.Format("2006-01-02 15:04"), a.Source)
				}
				return nil
			case "diff":
				against := doc.Version
				if len(args) > 1 {
					against = args[1]
				}
				summary, derr := legal.ChangesSince(legal.KindTerms, against)
				if derr != nil {
					return derr
				}
				fmt.Fprintln(out, summary)
				return nil
			case "accept":
				return acceptTerms(c, "cli")
			default:
				return errs.Usage("unknown terms subcommand %q (try current, history, diff, accept)", sub)
			}
		},
	}
}

// acceptTerms records consent in the local ledger. It is deliberately separate
// from reading the Terms so that acceptance is always an explicit act.
func acceptTerms(c *cli.Context, source string) error {
	acc, err := recordTermsAcceptance(source)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.App.Stdout, "accepted Terms %s (sha256 %s)\nrecorded in %s\n",
		acc.Version, acc.SHA256[:12], privacy.LegalPath())
	return nil
}

// recordTermsAcceptance writes the consent record and audits it.
func recordTermsAcceptance(source string) (legal.Acceptance, error) {
	ledger, err := legal.OpenLedger(privacy.LegalPath())
	if err != nil {
		return legal.Acceptance{}, err
	}
	doc, err := legal.CurrentTerms()
	if err != nil {
		return legal.Acceptance{}, err
	}
	acc, err := ledger.Accept(doc, source)
	if err != nil {
		return legal.Acceptance{}, err
	}
	log, _ := audit.Open(audit.Options{Path: privacy.AuditPath(), Enabled: true, Level: audit.LevelNormal})
	if log != nil {
		defer log.Close()
		log.Consent(acc.Version, legal.KindTerms)
	}
	return acc, nil
}

// acceptTermsFlag records acceptance without a prompt, for --accept-terms.
func acceptTermsFlag() error {
	_, err := recordTermsAcceptance("flag")
	return err
}

func consentStatus(kind string) (legal.Status, error) {
	ledger, err := legal.OpenLedger(privacy.LegalPath())
	if err != nil {
		return legal.Status{}, err
	}
	return ledger.Check(kind)
}

const privacyHelp = `
Shows the Privacy Notice and the data Talon keeps on this machine.

Subcommands:
  notice               print the Privacy Notice (default)
  data                 list what is stored, where, and how large
  clear <category>     delete one category (sessions, memory, logs, audit, …)
  clear all            delete every user-scoped category
  forget               delete the consent ledger

Talon has no telemetry. Deleting data is irreversible and always asks first.
`

func privacyCommand() *cli.Command {
	var current bool
	var includeAll bool
	return &cli.Command{
		Name:     "privacy",
		Summary:  "show the Privacy Notice and manage stored data",
		Detailed: strings.TrimSpace(privacyHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&current, "current", false, "print the Privacy Notice in this build and exit")
			fs.BoolVar(&includeAll, "all", false, "with clear: also remove plugins and project-local data")
		},
		Run: func(c *cli.Context, args []string) error {
			sub := "notice"
			if current {
				sub = "notice"
			} else if len(args) > 0 {
				sub = args[0]
			}
			root, err := c.App.Globals().WorkingDir()
			if err != nil {
				return err
			}
			switch sub {
			case "notice", "current":
				doc, derr := legal.CurrentPrivacy()
				if derr != nil {
					return derr
				}
				fmt.Fprintf(c.App.Stdout, "Talon Privacy Notice %s (effective %s)\n\n",
					doc.Version, doc.EffectiveAt)
				fmt.Fprint(c.App.Stdout, strings.TrimRight(doc.Body, "\n")+"\n")
				return nil
			case "data", "list":
				printInventory(c, root)
				return nil
			case "clear":
				what := "all"
				if len(args) > 1 {
					what = args[1]
				}
				return clearCategories(c, root, what, includeAll)
			case "forget":
				if !confirmed(c, "Delete the consent ledger (Terms acceptance history)?") {
					fmt.Fprintln(c.App.Stdout, "cancelled")
					return nil
				}
				ledger, lerr := legal.OpenLedger(privacy.LegalPath())
				if lerr != nil {
					return lerr
				}
				if rerr := ledger.Remove(); rerr != nil {
					return rerr
				}
				fmt.Fprintln(c.App.Stdout, "consent ledger removed")
				return nil
			default:
				return errs.Usage("unknown privacy subcommand %q (try notice, data, clear, forget)", sub)
			}
		},
	}
}

func printInventory(c *cli.Context, root string) {
	items := privacy.Inventory(root)
	fmt.Fprintln(c.App.Stdout, "Data stored on this machine")
	width := 0
	for _, item := range items {
		if n := len(item.Category.Label()); n > width {
			width = n
		}
	}
	for _, item := range items {
		fmt.Fprintf(c.App.Stdout, "  %-*s  %s\n", width, item.Category.Label(), item.Human())
	}
	count, size := privacy.Total(items)
	fmt.Fprintf(c.App.Stdout, "\n%d item(s), %s total. Nothing is sent anywhere except to your model provider.\n",
		count, humanBytes(size))
	fmt.Fprintln(c.App.Stdout, "Delete with: talon privacy clear <category>")
}

func clearCategories(c *cli.Context, root, what string, includeAll bool) error {
	req := privacy.ClearRequest{ProjectRoot: root, IncludePlugins: includeAll}
	if what != "all" {
		cat, err := privacy.ParseCategory(what)
		if err != nil {
			return errs.Usage("unknown category %q (try all, or one of the names talon privacy data lists)", what)
		}
		req.Categories = []privacy.Category{cat}
	}
	plan := privacy.BuildPlan(req)
	if len(plan.Items) == 0 {
		fmt.Fprintln(c.App.Stdout, "nothing to delete")
		return nil
	}
	if !confirmed(c, fmt.Sprintf("Permanently delete %s of data (%s)? This cannot be undone.",
		humanBytes(plan.Bytes()), categorySummary(plan))) {
		fmt.Fprintln(c.App.Stdout, "cancelled")
		return nil
	}
	if err := privacy.Apply(plan); err != nil {
		return err
	}
	log, _ := audit.Open(audit.Options{Path: privacy.AuditPath(), Enabled: true, Level: audit.LevelNormal})
	if log != nil {
		defer log.Close()
		log.DataRemoved(categorySummary(plan))
	}
	fmt.Fprintf(c.App.Stdout, "deleted %s\n", categorySummary(plan))
	return nil
}

func categorySummary(plan privacy.Plan) string {
	names := make([]string, 0, len(plan.Items))
	for _, item := range plan.Items {
		names = append(names, item.Category.Label())
	}
	return strings.Join(names, ", ")
}

const securityHelp = `
Shows the security controls that are in force for this directory.

Nothing here can be changed from the command: the posture comes from the
configuration, so what is printed is what runs. Use talon config set to change a
control deliberately.

This build contains no telemetry and no crash reporting.
`

func securityCommand() *cli.Command {
	return &cli.Command{
		Name:     "security",
		Summary:  "show the security controls in force",
		Detailed: strings.TrimSpace(securityHelp),
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			posture, perr := secure.New(secure.Options{Config: cfg, Workspace: cfg.ProjectRoot})
			if perr != nil {
				return perr
			}
			defer posture.Close()
			fmt.Fprint(c.App.Stdout, posture.Summary())
			for _, item := range privacy.Limitations() {
				fmt.Fprintf(c.App.Stdout, "  - %s\n", item)
			}
			return nil
		},
	}
}

func sandboxCommand() *cli.Command {
	return &cli.Command{
		Name:    "sandbox",
		Summary: "report the kernel sandbox available for commands",
		Detailed: strings.TrimSpace(`
Reports what the kernel can enforce for commands Talon runs.

On Linux with Landlock available, commands are confined to the workspace and the
tool directories. Where no kernel sandbox exists, Talon says so instead of
pretending; set security.sandbox_required = true to refuse commands instead.
`),
		Run: func(c *cli.Context, args []string) error {
			status := sandbox.Detect()
			fmt.Fprintln(c.App.Stdout, strings.TrimRight(status.String(), "\n"))
			return nil
		},
	}
}

func auditCommand() *cli.Command {
	var count int
	var showPath bool
	return &cli.Command{
		Name:    "audit",
		Summary: "show the local security audit log",
		Detailed: strings.TrimSpace(`
Prints recent entries from the local audit log: tool decisions, approvals,
network requests, redactions, injection warnings and consent records.

The log is append-only JSONL on this machine, mode 0600, and credentials are
redacted before anything is written.
`),
		Setup: func(fs *flag.FlagSet) {
			fs.IntVar(&count, "n", 20, "how many entries to show")
			fs.BoolVar(&showPath, "path", false, "print the log location only")
		},
		Run: func(c *cli.Context, args []string) error {
			path := privacy.AuditPath()
			if showPath {
				fmt.Fprintln(c.App.Stdout, path)
				return nil
			}
			entries, err := audit.Read(path, count)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintln(c.App.Stdout, "the audit log is empty")
				return nil
			}
			for _, e := range entries {
				fmt.Fprintln(c.App.Stdout, e.String())
			}
			return nil
		},
	}
}

func dataCommand() *cli.Command {
	var all bool
	return &cli.Command{
		Name:     "data",
		Summary:  "list or clear the data Talon stores",
		Detailed: strings.TrimSpace(privacyHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&all, "all", false, "also remove plugins and the project-local .talon directory")
		},
		Run: func(c *cli.Context, args []string) error {
			root, err := c.App.Globals().WorkingDir()
			if err != nil {
				return err
			}
			if len(args) == 0 || args[0] == "list" {
				printInventory(c, root)
				return nil
			}
			if args[0] == "clear" {
				what := "all"
				if len(args) > 1 {
					what = args[1]
				}
				return clearCategories(c, root, what, all)
			}
			return errs.Usage("unknown data subcommand %q (try list, clear)", args[0])
		},
	}
}

// historyCommand deletes the local input history.
func historyCommand() *cli.Command {
	return &cli.Command{
		Name:    "history",
		Summary: "clear the local input history",
		Detailed: strings.TrimSpace(`
Removes the readline input history file Talon keeps under its state directory.

This is the only local record of what you typed at the prompt; conversation
transcripts are managed with talon data clear sessions.
`),
		Run: func(c *cli.Context, args []string) error {
			path := paths.HistoryFile()
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Fprintln(c.App.Stdout, "no input history stored")
				return nil
			}
			if !confirmed(c, fmt.Sprintf("Delete the input history (%s)?", path)) {
				fmt.Fprintln(c.App.Stdout, "cancelled")
				return nil
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			fmt.Fprintln(c.App.Stdout, "input history deleted")
			return nil
		},
	}
}

// confirmed asks for a yes on stdin. Anything else, including EOF, means no.
func confirmed(c *cli.Context, question string) bool {
	fmt.Fprintf(c.App.Stderr, "%s [y/N] ", question)
	var answer string
	if _, err := fmt.Fscanln(c.App.Stdin, &answer); err != nil {
		fmt.Fprintln(c.App.Stderr)
		return false
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
