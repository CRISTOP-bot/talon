package commands

import (
	"flag"
	"fmt"
	"runtime"

	"github.com/talon-cli/talon/internal/app"
	"github.com/talon-cli/talon/internal/cli"
)

// versionCommand prints build metadata.
func versionCommand() *cli.Command {
	var short bool
	return &cli.Command{
		Name:    "version",
		Summary: "print version and build information",
		Setup: func(fs *flag.FlagSet) {
			fs.BoolVar(&short, "short", false, "print only the version number")
		},
		Run: func(c *cli.Context, args []string) error {
			if short {
				fmt.Fprintln(c.App.Stdout, app.Version)
				return nil
			}
			fmt.Fprintf(c.App.Stdout, "%s %s\n", app.Name, app.Version)
			fmt.Fprintf(c.App.Stdout, "  commit:  %s\n", app.Commit)
			fmt.Fprintf(c.App.Stdout, "  built:   %s\n", app.Date)
			fmt.Fprintf(c.App.Stdout, "  go:      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
