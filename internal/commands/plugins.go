package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/logger"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/plugin"
	"github.com/CRISTOP-bot/talon/internal/ui"
)

const pluginsHelp = `
Talon plugins are separate executables that speak JSON-RPC over stdin/stdout.
They can add tools without changing Talon itself.

  talon plugins list                installed plugins and their tools
  talon plugins install <dir|url>    install from a local directory or a git URL
  talon plugins remove <name>        uninstall a plugin
  talon plugins test <dir>           run one plugin without installing it

A plugin directory contains talon-plugin.json, which names the executable to
run. Talon never executes scripts downloaded from the network: installing from a
git URL clones the repository and then runs only the executable the manifest
declares, and you can inspect it first with "talon plugins test".
`

func pluginsCommand() *cli.Command {
	return &cli.Command{
		Name:     "plugins",
		Summary:  "manage plugins",
		Detailed: strings.TrimSpace(pluginsHelp),
		Setup:    func(fs *flag.FlagSet) {},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName(cfg.UI.Theme))
			sub := "list"
			if len(args) > 0 {
				sub = args[0]
			}
			rest := args[1:]
			switch sub {
			case "list", "":
				return pluginsList(c, cfg, pr)
			case "install":
				return pluginsInstall(c, cfg, pr, rest)
			case "remove", "uninstall":
				return pluginsRemove(c, cfg, pr, rest)
			case "test":
				return pluginsTest(c, pr, rest)
			default:
				return errs.Usage("unknown plugins subcommand %q (list, install, remove, test)", sub)
			}
		},
	}
}

func pluginsList(c *cli.Context, cfg *config.Config, pr *ui.Printer) error {
	dirs, err := plugin.Discover(cfg.Plugins.Dir)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		pr.Muted("no plugins installed in " + cfg.Plugins.Dir)
		pr.Muted("install one with: talon plugins install <dir|url>")
		return nil
	}
	log := logger.Discard
	for _, dir := range dirs {
		manifest, merr := plugin.LoadManifest(dir)
		if merr != nil {
			pr.Failure(fmt.Sprintf("%s: %s", filepath.Base(dir), errs.User(merr)))
			continue
		}
		pr.Info(fmt.Sprintf("%s %s — %s", manifest.Name, manifest.Version, manifest.Description))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		p, err := plugin.Install(ctx, dir, log)
		cancel()
		if err != nil {
			pr.Warn("  " + errs.User(err))
			continue
		}
		for _, t := range p.Tools() {
			pr.Printf("    %-28s %s\n", p.QualifiedName(t.Name), firstLine(t.Description))
		}
		_ = p.Stop()
	}
	return nil
}

func pluginsInstall(c *cli.Context, cfg *config.Config, pr *ui.Printer, args []string) error {
	if len(args) == 0 {
		return errs.Usage("usage: talon plugins install <dir|url>")
	}
	source := args[0]
	if err := paths.EnsureDir(cfg.Plugins.Dir); err != nil {
		return errs.Wrap(errs.KindPermission, "plugins", err)
	}

	var target string
	if isRemote(source) {
		name := repoName(source)
		target = filepath.Join(cfg.Plugins.Dir, name)
		if _, err := os.Stat(target); err == nil {
			return errs.Newf(errs.KindUsage, "plugins", "%s is already installed", name)
		}
		pr.Muted("cloning " + source)
		if err := gitClone(source, target); err != nil {
			return err
		}
	} else {
		abs, aerr := filepath.Abs(source)
		if aerr != nil {
			return errs.Newf(errs.KindConfig, "plugins", "cannot resolve %s", source)
		}
		manifest, merr := plugin.LoadManifest(abs)
		if merr != nil {
			return merr
		}
		target = filepath.Join(cfg.Plugins.Dir, manifest.Name)
		if sameDir(abs, target) {
			pr.Success(manifest.Name + " is already the plugin directory")
			return nil
		}
		if err := copyTree(abs, target); err != nil {
			return err
		}
	}

	manifest, err := plugin.LoadManifest(target)
	if err != nil {
		return err
	}
	pr.Success(fmt.Sprintf("installed %s %s into %s", manifest.Name, manifest.Version, target))
	pr.Muted("run the plugin with `talon plugins test " + target + "` to verify it before using it")
	return nil
}

func pluginsRemove(c *cli.Context, cfg *config.Config, pr *ui.Printer, args []string) error {
	if len(args) == 0 {
		return errs.Usage("usage: talon plugins remove <name>")
	}
	target := filepath.Join(cfg.Plugins.Dir, args[0])
	if _, err := os.Stat(target); err != nil {
		return errs.NotFound("plugins", "%s is not installed", args[0])
	}
	if err := os.RemoveAll(target); err != nil {
		return errs.Wrap(errs.KindPermission, "plugins", err)
	}
	pr.Success("removed " + args[0])
	return nil
}

func pluginsTest(c *cli.Context, pr *ui.Printer, args []string) error {
	if len(args) == 0 {
		return errs.Usage("usage: talon plugins test <dir>")
	}
	abs, err := filepath.Abs(args[0])
	if err != nil {
		return errs.Newf(errs.KindConfig, "plugins", "cannot resolve %s", args[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := plugin.Install(ctx, abs, logger.Discard)
	if err != nil {
		return err
	}
	defer p.Stop()

	pr.Success(fmt.Sprintf("%s %s connected", p.Manifest.Name, p.Manifest.Version))
	toolsList := p.Tools()
	if len(toolsList) == 0 {
		pr.Muted("this plugin exposes no tools")
		return nil
	}
	rows := make([][]string, 0, len(toolsList))
	for _, t := range toolsList {
		rows = append(rows, []string{p.QualifiedName(t.Name), firstLine(t.Description)})
	}
	pr.Table([]string{"tool", "description"}, rows)
	return nil
}

// isRemote reports whether the source looks like a git URL.
func isRemote(source string) bool {
	return strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "git@") ||
		strings.HasPrefix(source, "http://")
}

// repoName derives the plugin name from a git URL.
func repoName(source string) string {
	trimmed := strings.TrimSuffix(source, ".git")
	if i := strings.LastIndexAny(trimmed, "/:"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return strings.ToLower(trimmed)
}

// gitClone clones a repository into dir without running any hook or script.
func gitClone(source, dir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errs.NotFound("plugins", "git is required to install from a URL")
	}
	cmd := exec.Command("git", "clone", "--depth", "1", source, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dir)
		return errs.Newf(errs.KindNetwork, "plugins", "git clone failed: %s",
			strings.TrimSpace(string(out)))
	}
	return nil
}

// sameDir reports whether two paths point at the same directory.
func sameDir(a, b string) bool {
	sa, err1 := filepath.EvalSymlinks(a)
	sb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return sa == sb
}

// copyTree copies a directory recursively.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
