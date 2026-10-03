package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CRISTOP-bot/talon/internal/app"
	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/session"
	"github.com/CRISTOP-bot/talon/internal/ui"
	"github.com/CRISTOP-bot/talon/internal/updater"
)

const updateHelp = `
Checks GitHub for a newer Talon release and replaces the running binary.

  talon update              download and install the latest release
  talon update --check      only report whether an update exists
  talon update --version X  install a specific tag (e.g. v0.2.0)

Safety: only assets published by the configured repository are downloaded, they
are verified against the release checksum file, and no downloaded script is ever
executed. The previous binary is kept as <name>.old.
`

func updateCommand() *cli.Command {
	var checkOnly bool
	var version string
	var repo string
	return &cli.Command{
		Name:     "update",
		Summary:  "update Talon to the latest release",
		Detailed: strings.TrimSpace(updateHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&checkOnly, "check", false, "only check whether an update is available")
			fs.StringVar(&version, "version", "", "install this tag instead of the latest release")
			fs.StringVar(&repo, "repo", "", "release repository (owner/name)")
		},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			if repo != "" {
				cfg.Updater.Repo = repo
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName(cfg.UI.Theme))

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			u := updater.New(cfg.Updater.Repo)
			rel, err := u.Latest(ctx)
			if err != nil {
				return err
			}
			if version != "" {
				found, ferr := findRelease(ctx, u, version)
				if ferr != nil {
					return ferr
				}
				rel = found
			}
			if rel.Draft {
				pr.Muted("note: the latest release is a draft")
			}
			current := app.Version
			latest := strings.TrimPrefix(rel.TagName, "v")
			switch updater.CompareVersions(latest, current) {
			case 0:
				pr.Success("already up to date (" + current + ")")
				return nil
			case -1:
				pr.Muted(fmt.Sprintf("the published release (%s) is older than this build (%s)", latest, current))
				if !checkOnly {
					pr.Muted("use --version to install a specific release")
				}
				return nil
			}

			asset, ok := u.AssetFor(rel)
			if !ok {
				return errs.NotFound("update",
					"release %s has no asset for %s/%s", rel.TagName, runtime.GOOS, runtime.GOARCH)
			}
			if checkOnly {
				pr.Info(fmt.Sprintf("update available: %s → %s (%s)", current, latest, asset.Name))
				pr.Muted("run `talon update` to install it")
				return nil
			}

			pr.Muted("downloading " + asset.Name)
			data, err := u.Download(ctx, asset)
			if err != nil {
				return err
			}
			if cs, ok := u.Checksums(rel); ok {
				sums, derr := u.Download(ctx, cs)
				if derr != nil {
					return derr
				}
				if err := updater.Verify(data, sums, asset.Name); err != nil {
					return err
				}
				pr.Muted("checksum verified")
			} else {
				pr.Warn("the release publishes no checksum file; install without verification")
			}

			target, terr := os.Executable()
			if terr != nil {
				target = filepath.Join(filepath.Dir(os.Args[0]), app.Name)
			}
			target, _ = filepath.EvalSymlinks(target)
			if err := updater.Install(data, target); err != nil {
				return err
			}
			pr.Success(fmt.Sprintf("installed %s into %s", latest, target))
			pr.Muted("the previous binary was kept as " + filepath.Base(target) + ".old")
			fmt.Fprintln(c.App.Stderr)
			return nil
		},
	}
}

// findRelease looks up a specific tag through the same API base.
func findRelease(ctx context.Context, u *updater.Updater, tag string) (*updater.Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", strings.TrimRight(u.APIBase, "/"), u.Repo, tag)
	data, err := u.Download(ctx, updater.Asset{URL: url})
	if err != nil {
		return nil, err
	}
	var rel updater.Release
	if derr := json.Unmarshal(data, &rel); derr != nil {
		return nil, errs.Parse("update", "the release API returned malformed JSON: %v", derr)
	}
	return &rel, nil
}

const sessionsHelp = `
Lists and inspects saved sessions.

  talon sessions list            sessions for the current project
  talon sessions list --all      sessions from every project
  talon sessions show <id>       print a session transcript
  talon sessions delete <id>     remove a session
`

func sessionsCommand() *cli.Command {
	var all bool
	return &cli.Command{
		Name:     "sessions",
		Summary:  "list and inspect saved sessions",
		Detailed: strings.TrimSpace(sessionsHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&all, "all", false, "include sessions from other projects")
		},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName(cfg.UI.Theme))
			root, werr := c.App.Globals().WorkingDir()
			if werr != nil {
				return werr
			}
			store := session.NewStore("", cfg.Sessions.Max)
			sub := "list"
			if len(args) > 0 {
				sub = args[0]
			}
			rest := args[1:]
			switch sub {
			case "list", "":
				project := root
				if all {
					project = ""
				}
				list, lerr := store.List(project)
				if lerr != nil {
					return lerr
				}
				if len(list) == 0 {
					pr.Muted("no sessions saved yet")
					return nil
				}
				rows := make([][]string, 0, len(list))
				for _, s := range list {
					projectLabel := s.Project
					if !all {
						projectLabel = "."
					} else if len(projectLabel) > 24 {
						projectLabel = "…" + projectLabel[len(projectLabel)-23:]
					}
					rows = append(rows, []string{
						s.ID,
						s.Updated.Local().Format("2006-01-02 15:04"),
						projectLabel,
						fmt.Sprintf("%d", s.MessageCount()),
						truncate(s.Summary, 50),
					})
				}
				pr.Table([]string{"id", "updated", "project", "turns", "summary"}, rows)
			case "show":
				if len(rest) == 0 {
					return errs.Usage("usage: talon sessions show <id>")
				}
				s, lerr := store.Load(rest[0])
				if lerr != nil {
					return lerr
				}
				pr.Printf("%s  %s  %d messages\n\n", s.ID, s.Project, len(s.Entries))
				for _, e := range s.Entries {
					switch e.Role {
					case "user":
						pr.Line(pr.WrapText("you: " + e.Content))
					case "assistant":
						if e.Content != "" {
							pr.Line(pr.WrapText("talon: " + e.Content))
						}
						for _, c := range e.ToolCalls {
							pr.Muted(fmt.Sprintf("  tool: %s(%s)", c.Name, truncate(c.Arguments, 100)))
						}
					case "tool":
						pr.Muted(fmt.Sprintf("  %s → %s", e.Name, truncate(e.Content, 120)))
					}
				}
			case "delete":
				if len(rest) == 0 {
					return errs.Usage("usage: talon sessions delete <id>")
				}
				if derr := store.Delete(rest[0]); derr != nil {
					return derr
				}
				pr.Success("deleted " + rest[0])
			default:
				return errs.Usage("unknown subcommand %q (list, show, delete)", sub)
			}
			return nil
		},
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
