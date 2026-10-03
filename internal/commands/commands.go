// Package commands wires Talon's CLI verbs onto the dispatcher.
package commands

import (
	"github.com/talon-cli/talon/internal/cli"
	"github.com/talon-cli/talon/internal/config"
	"github.com/talon-cli/talon/internal/tools"
)

// Register adds every command to the app.
func Register(a *cli.App) {
	a.Command(versionCommand())
	a.Command(initCommand())
	a.Command(configCommand())
	a.Command(doctorCommand())
	a.Command(modelsCommand())
	a.Command(toolsCommand())
	a.Command(permissionsCommand())
	a.Command(pluginsCommand())
	a.Command(sessionsCommand())
	a.Command(termsCommand())
	a.Command(privacyCommand())
	a.Command(securityCommand())
	a.Command(sandboxCommand())
	a.Command(auditCommand())
	a.Command(dataCommand())
	a.Command(historyCommand())
	a.Command(updateCommand())
}

// allToolDefinitions returns the built-in tool definitions, used by `talon tools`.
func allToolDefinitions() []*tools.Definition { return tools.All() }

// loadConfig resolves the working directory and loads configuration for it.
func loadConfig(ctx *cli.Context) (*config.Config, error) {
	root, err := ctx.App.Globals().WorkingDir()
	if err != nil {
		return nil, err
	}
	return config.Load(root)
}
