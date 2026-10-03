package commands

import (
	"flag"
	"fmt"
	"strings"

	"github.com/talon-cli/talon/internal/cli"
	"github.com/talon-cli/talon/internal/config"
	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/toml"
)

const configHelp = `
Reads and writes Talon's configuration.

Subcommands:
  list                 print the effective configuration
  get <key>            print one value
  set <key> <value>    write one value and save it
  unset <key>          remove one value and save it
  path                 print the config files in use

Values are TOML, so strings must be quoted: set model.name "gpt-5-codex".
Writes go to the user file by default; use --project to write .talon/config.toml.
`

func configCommand() *cli.Command {
	var project bool
	var showSecrets bool
	return &cli.Command{
		Name:     "config",
		Summary:  "read and write configuration",
		Detailed: strings.TrimSpace(configHelp),
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&project, "project", false, "write to .talon/config.toml instead of the user file")
			fs.BoolVar(&showSecrets, "show-secrets", false, "print api keys in plain text (not recommended)")
		},
		Run: func(c *cli.Context, args []string) error {
			cfg, err := loadConfig(c)
			if err != nil {
				return err
			}
			sub := "list"
			if len(args) > 0 {
				sub = args[0]
			}
			switch sub {
			case "list":
				return configList(c, cfg, showSecrets)
			case "get":
				if len(args) < 2 {
					return errs.Usage("usage: talon config get <key>")
				}
				return configGet(c, cfg, args[1])
			case "set":
				if len(args) < 3 {
					return errs.Usage("usage: talon config set <key> <value>")
				}
				return configSet(c, cfg, args[1], strings.Join(args[2:], " "), project)
			case "unset":
				if len(args) < 2 {
					return errs.Usage("usage: talon config unset <key>")
				}
				return configUnset(c, cfg, args[1], project)
			case "path":
				fmt.Fprintf(c.App.Stdout, "user:    %s\n", cfg.UserFile)
				fmt.Fprintf(c.App.Stdout, "project: %s\n", cfg.ProjectFile)
				fmt.Fprintf(c.App.Stdout, "active:  %s\n", cfg.EffectiveModel())
				return nil
			default:
				return errs.Usage("unknown config subcommand %q (try list, get, set, unset, path)", sub)
			}
		},
	}
}

func configList(c *cli.Context, cfg *config.Config, showSecrets bool) error {
	var b strings.Builder
	b.WriteString("Effective configuration\n\n")
	b.WriteString(fmt.Sprintf("  project root: %s\n", cfg.ProjectRoot))
	b.WriteString(fmt.Sprintf("  user file:    %s\n", cfg.UserFile))
	b.WriteString(fmt.Sprintf("  project file: %s\n", cfg.ProjectFile))
	b.WriteString("\n")

	apiKey := "(unset)"
	if showSecrets {
		if k := cfg.APIKeyValue(); k != "" {
			apiKey = k
		}
	} else {
		apiKey = maskKey(cfg.APIKeyValue())
	}
	pairs := [][2]string{
		{"model.provider", cfg.Model.Provider},
		{"model.name", orDash(cfg.Model.Name)},
		{"model.base_url", orDash(cfg.Model.BaseURL)},
		{"model.api_key", apiKey},
		{"model.temperature", fmt.Sprintf("%g", cfg.Model.Temperature)},
		{"model.max_tokens", fmt.Sprintf("%d", cfg.Model.MaxTokens)},
		{"model.timeout_seconds", fmt.Sprintf("%d", cfg.Model.TimeoutSeconds)},
		{"agent.max_steps", fmt.Sprintf("%d", cfg.Agent.MaxSteps)},
		{"agent.max_tool_output", fmt.Sprintf("%d", cfg.Agent.MaxToolOutput)},
		{"context.max_bytes", fmt.Sprintf("%d", cfg.Context.MaxBytes)},
		{"context.max_files", fmt.Sprintf("%d", cfg.Context.MaxFiles)},
		{"context.respect_gitignore", fmt.Sprintf("%t", cfg.Context.RespectGitignore)},
		{"ui.theme", cfg.UI.Theme},
		{"ui.stream", fmt.Sprintf("%t", cfg.UI.Stream)},
		{"ui.spinner", fmt.Sprintf("%t", cfg.UI.Spinner)},
		{"permissions.level", cfg.Permissions.Level},
		{"permissions.deny_commands", joinOrNone(cfg.Permissions.DenyCommands)},
		{"memory.enabled", fmt.Sprintf("%t", cfg.Memory.Enabled)},
		{"sessions.enabled", fmt.Sprintf("%t", cfg.Sessions.Enabled)},
		{"log.level", cfg.Logging.Level},
		{"plugins.enabled", fmt.Sprintf("%t", cfg.Plugins.Enabled)},
		{"mcp.enabled", fmt.Sprintf("%t", cfg.MCP.Enabled)},
	}
	for _, p := range pairs {
		fmt.Fprintf(&b, "  %-30s %s\n", p[0], p[1])
	}
	if len(cfg.Warnings) > 0 {
		b.WriteString("\nWarnings\n")
		for _, w := range cfg.Warnings {
			fmt.Fprintf(&b, "  ! %s\n", w)
		}
	}
	fmt.Fprint(c.App.Stdout, b.String())
	return nil
}

func configGet(c *cli.Context, cfg *config.Config, key string) error {
	v, ok := cfg.Doc().Get(key)
	if !ok {
		return errs.NotFound("config", "key %q is not set", key)
	}
	if v.Kind == toml.KindTable {
		var b strings.Builder
		for _, k := range v.Keys() {
			fmt.Fprintf(&b, "%s.%s = %s\n", key, k, v.Table[k].String())
		}
		fmt.Fprint(c.App.Stdout, b.String())
		return nil
	}
	fmt.Fprintln(c.App.Stdout, v.String())
	return nil
}

func configSet(c *cli.Context, cfg *config.Config, key, raw string, project bool) error {
	if err := cfg.SetIn(key, raw, scopeOf(project)); err != nil {
		return err
	}
	if project {
		if err := cfg.SaveProject(); err != nil {
			return err
		}
		fmt.Fprintf(c.App.Stdout, "set %s in %s\n", key, cfg.ProjectFile)
		return nil
	}
	if err := cfg.SaveUser(); err != nil {
		return err
	}
	fmt.Fprintf(c.App.Stdout, "set %s in %s\n", key, cfg.UserFile)
	return nil
}

func configUnset(c *cli.Context, cfg *config.Config, key string, project bool) error {
	if err := cfg.UnsetIn(key, scopeOf(project)); err != nil {
		return err
	}
	if project {
		if err := cfg.SaveProject(); err != nil {
			return err
		}
		fmt.Fprintf(c.App.Stdout, "unset %s in %s\n", key, cfg.ProjectFile)
		return nil
	}
	if err := cfg.SaveUser(); err != nil {
		return err
	}
	fmt.Fprintf(c.App.Stdout, "unset %s in %s\n", key, cfg.UserFile)
	return nil
}

func scopeOf(project bool) string {
	if project {
		return "project"
	}
	return "user"
}

func orDash(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "(empty)"
	}
	return strings.Join(items, "; ")
}

// maskKey shows only the tail of a credential.
func maskKey(k string) string {
	if k == "" {
		return "(unset)"
	}
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return "…" + k[len(k)-4:]
}
