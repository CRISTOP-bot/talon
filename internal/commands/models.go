package commands

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/ui"
)

const modelsHelp = `
Lists the models a provider offers and lets you switch between them.

  talon models                 list the models of the configured provider
  talon models --provider X    list the models of another provider
  talon models --set <id>      use <id> as the configured model
  talon models --offline       only show the built-in defaults (no network)
`

func modelsCommand() *cli.Command {
	var provider string
	var set string
	var offline bool
	return &cli.Command{
		Name:     "models",
		Summary:  "list and select models",
		Detailed: strings.TrimSpace(modelsHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.StringVar(&provider, "provider", "", "provider to query")
			fs.StringVar(&set, "set", "", "set this model id as the configured model")
			fs.BoolVar(&offline, "offline", false, "skip the network and show built-in defaults")
		},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			if provider != "" {
				cfg.SetIn("model.provider", quoteTOML(provider), "user")
			}
			if len(args) > 0 && set == "" {
				set = args[0]
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName(cfg.UI.Theme))

			if set != "" {
				if err := cfg.SetIn("model.name", quoteTOML(set), "user"); err != nil {
					return err
				}
				if err := cfg.SaveUser(); err != nil {
					return err
				}
				pr.Success("model set to " + cfg.EffectiveModel())
				return nil
			}

			var models []llm.ModelInfo
			if !offline {
				p, perr := llm.New(cfg.Model.Provider, llm.Options{
					APIKey:  cfg.APIKeyValue(),
					BaseURL: cfg.Model.BaseURL,
					Model:   cfg.Model.Name,
				})
				if perr != nil {
					pr.Warn(errs.User(perr))
				} else {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					listed, lerr := p.ListModels(ctx)
					if lerr != nil {
						pr.Warn("could not reach the provider: " + errs.User(lerr))
					} else {
						models = listed
					}
				}
			}
			if len(models) == 0 {
				models = llm.Catalog(cfg.Model.Provider)
				pr.Muted("showing the built-in defaults for " + cfg.Model.Provider)
			}
			llm.SortModels(models)
			rows := make([][]string, 0, len(models))
			for _, m := range models {
				marker := " "
				if m.ID == cfg.Model.Name {
					marker = "*"
				}
				window := ""
				if m.ContextWindow > 0 {
					window = fmt.Sprintf("%d", m.ContextWindow)
				}
				rows = append(rows, []string{marker, m.DisplayName, m.ID, window})
			}
			pr.Table([]string{"", "name", "id", "context"}, rows)
			pr.Muted("select one with: talon models --set <id>")
			pr.Muted("providers: " + strings.Join(llm.Providers(), ", "))
			return nil
		},
	}
}

const toolsHelp = `
Lists the tools the agent can use, with their risk level and where they come from.

  talon tools              list every registered tool
  talon tools --describe   include the full description sent to the model
`

func toolsCommand() *cli.Command {
	var describe bool
	return &cli.Command{
		Name:     "tools",
		Summary:  "list the tools the agent can use",
		Detailed: strings.TrimSpace(toolsHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&describe, "describe", false, "print the full tool descriptions")
		},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName(cfg.UI.Theme))
			rows := make([][]string, 0)
			for _, d := range allToolDefinitions() {
				desc := firstLine(d.Description)
				if describe {
					desc = d.Description
				}
				rows = append(rows, []string{d.Name, d.Risk.String(), d.Source, desc})
			}
			pr.Table([]string{"tool", "risk", "source", "description"}, rows)
			pr.Muted(fmt.Sprintf("%d tools", len(rows)))
			return nil
		},
	}
}

const permissionsHelp = `
Shows or changes the permission level, which controls what the agent may do
without asking.

  talon permissions                 show the effective policy
  talon permissions confirm         ask before writes and commands (default)
  talon permissions full-access     run everything automatically
  talon permissions read-only       only read operations
  talon permissions --save          write the level to the user configuration
`

func permissionsCommand() *cli.Command {
	var save bool
	return &cli.Command{
		Name:     "permissions",
		Summary:  "show or change the permission level",
		Detailed: strings.TrimSpace(permissionsHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&save, "save", false, "persist the level in the user configuration")
		},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			pr := ui.NewPrinter(c.App.Stdout, c.App.Stderr, ui.ThemeByName(cfg.UI.Theme))
			if len(args) == 0 {
				root, werr := c.App.Globals().WorkingDir()
				if werr != nil {
					return werr
				}
				policy := perm.New(perm.Config{
					Level:          perm.Level(cfg.Permissions.Level),
					Workspace:      root,
					AllowPaths:     cfg.Permissions.AllowPaths,
					DenyPaths:      cfg.Permissions.DenyPaths,
					DenyCommands:   cfg.Permissions.DenyCommands,
					AllowCommands:  cfg.Permissions.AllowCommands,
					AlwaysAskTools: cfg.Agent.AutoApproveTools,
				})
				pr.Line(pr.WrapText(policy.Summarize()))
				pr.Muted("levels: read-only, safe, confirm, full-access")
				return nil
			}
			level := strings.ToLower(strings.TrimSpace(args[0]))
			switch perm.Level(level) {
			case perm.ReadOnly, perm.Safe, perm.Confirm, perm.FullAccess:
			default:
				return errs.Usage("unknown level %q (read-only, safe, confirm, full-access)", args[0])
			}
			if err := cfg.SetIn("permissions.level", quoteTOML(level), "user"); err != nil {
				return err
			}
			if !save {
				pr.Success("level: " + level + " (not saved; add --save to persist it)")
				return nil
			}
			if err := cfg.SaveUser(); err != nil {
				return err
			}
			pr.Success("level: " + level + " (saved to " + cfg.UserFile + ")")
			return nil
		},
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}
