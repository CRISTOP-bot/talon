// Command talon is the Talon agentic coding CLI.
package main

import (
	"os"

	"github.com/talon-cli/talon/internal/cli"
	"github.com/talon-cli/talon/internal/commands"
)

func main() {
	a := cli.NewApp()
	a.Stdout = os.Stdout
	a.Stderr = os.Stderr
	commands.Register(a)
	cli.SetSessionRunner(commands.RunSession)
	os.Exit(a.Run(os.Args))
}
