// Command talon is the Talon agentic coding CLI.
package main

import (
	"os"

	"github.com/CRISTOP-bot/talon/internal/cli"
	"github.com/CRISTOP-bot/talon/internal/commands"
)

func main() {
	a := cli.NewApp()
	a.Stdout = os.Stdout
	a.Stderr = os.Stderr
	commands.Register(a)
	cli.SetSessionRunner(commands.RunSession)
	os.Exit(a.Run(os.Args))
}
