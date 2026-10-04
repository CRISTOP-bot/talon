// Command talon is the Talon agentic coding CLI.
package main

import (
	"os"

	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/commands"
	"github.com/CRISTOP-bot/talon/internal/sandbox"
)

func main() {
	// Talon re-executes itself to apply Landlock. The helper has to be handled
	// before flag parsing, otherwise the confinement never happens and the child
	// dies complaining about an unknown flag.
	if sandbox.IsHelperInvocation(os.Args[1:]) {
		os.Exit(sandbox.RunHelper(os.Args[1:]))
	}

	a := cli.NewApp()
	a.Stdout = os.Stdout
	a.Stderr = os.Stderr
	commands.Register(a)
	cli.SetSessionRunner(commands.RunSession)
	os.Exit(a.Run(os.Args))
}
