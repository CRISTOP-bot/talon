package commands

import (
	"flag"
	"fmt"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"os"
	"os/exec"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/paths"
)

const doctorHelp = `
Verifies that the environment is ready to run Talon: configuration, API key,
directories, git availability and terminal capabilities.

Exit code is 1 when a check fails, 2 when only warnings are present.
`

type checkStatus int

const (
	checkOK checkStatus = iota
	checkWarn
	checkFail
)

func (s checkStatus) label() string {
	switch s {
	case checkOK:
		return "ok  "
	case checkWarn:
		return "warn"
	default:
		return "FAIL"
	}
}

type check struct {
	name   string
	status checkStatus
	detail string
	fix    string
}

func doctorCommand() *cli.Command {
	return &cli.Command{
		Name:     "doctor",
		Summary:  "check that the environment is working",
		Detailed: strings.TrimSpace(doctorHelp),
		Setup:    func(fs *flag.FlagSet) {},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				fmt.Fprintf(c.App.Stdout, "FAIL  configuration\n      %v\n", errs.User(err))
				return err
			}
			checks := gatherChecks(cfg)
			worst := checkOK
			for _, ch := range checks {
				fmt.Fprintf(c.App.Stdout, "%s  %-22s %s\n", ch.status.label(), ch.name, ch.detail)
				if ch.fix != "" {
					fmt.Fprintf(c.App.Stdout, "      → %s\n", ch.fix)
				}
				if ch.status > worst {
					worst = ch.status
				}
			}
			switch worst {
			case checkFail:
				return errs.New(errs.KindConfig, "", "one or more doctor checks failed")
			case checkWarn:
				return nil
			}
			fmt.Fprintln(c.App.Stdout, "\nAll checks passed.")
			return nil
		},
	}
}

func gatherChecks(cfg *config.Config) []check {
	var out []check

	// Configuration files.
	out = append(out, check{
		name:   "config",
		status: checkOK,
		detail: fmt.Sprintf("user=%s project=%s", fileState(cfg.UserFile), fileState(cfg.ProjectFile)),
	})
	for _, w := range cfg.Warnings {
		out = append(out, check{name: "config warning", status: checkWarn, detail: w})
	}

	// API key / provider.
	provider := cfg.Model.Provider
	key := cfg.APIKeyValue()
	spec, known := llm.Spec(provider)
	switch {
	case known && spec.Local:
		if cfg.Model.Name == "" {
			out = append(out, check{
				name: "provider", status: checkWarn,
				detail: provider + " needs model.name",
				fix:    "talon config set model.name qwen2.5-coder",
			})
		} else {
			out = append(out, check{name: "provider", status: checkOK,
				detail: fmt.Sprintf("%s/%s (local)", provider, cfg.Model.Name)})
		}
	case provider == "custom":
		if cfg.Model.BaseURL == "" {
			out = append(out, check{
				name: "provider", status: checkWarn,
				detail: "custom provider needs model.base_url",
				fix:    "talon config set model.base_url http://localhost:1234/v1",
			})
		}
		if key == "" {
			out = append(out, check{
				name: "provider", status: checkWarn,
				detail: "custom provider needs an API key (or none if it is local)",
				fix:    "export AI_API_KEY=...",
			})
		}
	default:
		// Report the variable that is actually consulted for this provider, so
		// the fix line is copy-pasteable.
		vars := llm.KeyEnvFor(provider)
		if key == "" {
			out = append(out, check{
				name: "api key", status: checkWarn,
				detail: fmt.Sprintf("no key found in %s", dollarList(vars)),
				fix:    "export " + vars[0] + "=... or run `talon init`",
			})
		} else {
			out = append(out, check{name: "api key", status: checkOK,
				detail: fmt.Sprintf("present via %s", dollarList(vars))})
		}
		out = append(out, check{name: "model", status: checkOK,
			detail: fmt.Sprintf("%s/%s", provider, orDash(cfg.Model.Name))})
	}

	// Directories.
	for name, dir := range map[string]string{
		"config dir": paths.Config(),
		"data dir":   paths.Data(),
		"state dir":  paths.State(),
	} {
		if err := paths.EnsureDir(dir); err != nil {
			out = append(out, check{name: name, status: checkFail, detail: err.Error(),
				fix: "check the permissions of your home directory"})
			continue
		}
		if err := writable(dir); err != nil {
			out = append(out, check{name: name, status: checkFail, detail: err.Error()})
			continue
		}
		out = append(out, check{name: name, status: checkOK, detail: dir})
	}

	// Project root.
	if err := writable(cfg.ProjectRoot); err != nil {
		out = append(out, check{name: "project root", status: checkFail, detail: err.Error()})
	} else {
		out = append(out, check{name: "project root", status: checkOK, detail: cfg.ProjectRoot})
	}

	// Git.
	if gitBin, err := exec.LookPath("git"); err != nil {
		out = append(out, check{name: "git", status: checkWarn, detail: "git not found in PATH",
			fix: "install git to use repository-aware tools"})
	} else {
		st := checkOK
		detail := gitBin
		cmd := exec.Command(gitBin, "rev-parse", "--is-inside-work-tree")
		cmd.Dir = cfg.ProjectRoot
		if err := cmd.Run(); err != nil {
			st = checkWarn
			detail += " (current directory is not a git repository)"
		}
		out = append(out, check{name: "git", status: st, detail: detail})
	}

	// Shell.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "unknown (SHELL is not set; commands run through /bin/sh)"
	}
	out = append(out, check{name: "shell", status: checkOK, detail: shell})

	// Terminal.
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		out = append(out, check{name: "terminal", status: checkWarn,
			detail: "TERM=" + orDash(term) + " — colors and the spinner may be disabled"})
	} else {
		out = append(out, check{name: "terminal", status: checkOK, detail: term})
	}

	return out
}

func fileState(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%s (%d bytes)", path, info.Size())
}

// writable verifies that a directory accepts new files.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".talon-write-test-*")
	if err != nil {
		return errs.Permission("doctor", "cannot write to %s: %v", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return errs.Permission("doctor", "cannot clean up %s: %v", name, err)
	}
	return nil
}

// dollarList renders environment variable names for a message.
func dollarList(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "$"+n)
	}
	return strings.Join(out, " or ")
}
