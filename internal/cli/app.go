// Package cli implements Talon's command dispatcher: argument parsing, help
// rendering, exit codes and global flags.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/talon-cli/talon/internal/app"
	"github.com/talon-cli/talon/internal/errs"
)

// Command is a single CLI verb.
type Command struct {
	// Name is the primary name, e.g. "config".
	Name string
	// Aliases are alternative names.
	Aliases []string
	// Summary is the one-line description shown in `talon help`.
	Summary string
	// Usage overrides the generated usage line.
	Usage string
	// Detailed is the long description shown by `talon help <command>`.
	Detailed string
	// Setup registers flags on the command's flag set.
	Setup func(fs *flag.FlagSet)
	// Run executes the command with the remaining positional arguments.
	Run func(c *Context, args []string) error
	// Hidden keeps the command out of the help listing.
	Hidden bool
}

// matches reports whether name addresses this command.
func (cmd *Command) matches(name string) bool {
	if cmd.Name == name {
		return true
	}
	for _, a := range cmd.Aliases {
		if a == name {
			return true
		}
	}
	return false
}

// Globals holds the flags accepted before or after the subcommand name.
type Globals struct {
	Debug   bool
	Verbose bool
	Quiet   bool
	NoColor bool
	Version bool
	Cwd     string
	LogFile string
}

// App is the root of the command tree.
type App struct {
	// Stdout, Stderr and Stdin are injectable for tests.
	Stdout io.Writer
	Stderr io.Writer
	// Stdin is used by commands that must confirm an irreversible action. It
	// defaults to os.Stdin; commands must treat a read error as "no".
	Stdin io.Reader
	// NoColor disables ANSI styling globally.
	NoColor bool
	// Interactive reports whether a TUI session may be started.
	Interactive bool

	globals   Globals
	commands  []*Command
	byName    map[string]*Command
	globalSet *flag.FlagSet
}

// NewApp creates the root application.
func NewApp() *App {
	a := &App{
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		Stdin:       os.Stdin,
		byName:      map[string]*Command{},
		Interactive: true,
	}
	a.globalSet = flag.NewFlagSet("talon", flag.ContinueOnError)
	a.globalSet.SetOutput(io.Discard)
	a.globalSet.Usage = func() {}
	a.globalSet.BoolVar(&a.globals.Debug, "debug", false, "enable debug logging")
	a.globalSet.BoolVar(&a.globals.Verbose, "verbose", false, "verbose output")
	a.globalSet.BoolVar(&a.globals.Quiet, "quiet", false, "suppress non-essential output")
	a.globalSet.BoolVar(&a.globals.NoColor, "no-color", false, "disable ANSI colors")
	a.globalSet.BoolVar(&a.globals.Version, "version", false, "print version and exit")
	a.globalSet.StringVar(&a.globals.Cwd, "cwd", "", "directory to operate in (default: current)")
	a.globalSet.StringVar(&a.globals.LogFile, "log-file", "", "write logs to this file")
	return a
}

// Globals exposes the parsed global flags.
func (a *App) Globals() Globals { return a.globals }

// WorkingDir resolves the directory the CLI should operate in, honouring the
// --cwd global flag.
func (g Globals) WorkingDir() (string, error) {
	if g.Cwd != "" {
		abs, err := filepath.Abs(g.Cwd)
		if err != nil {
			return "", errs.Config("cli", "cannot resolve --cwd %q", g.Cwd)
		}
		info, serr := os.Stat(abs)
		if serr != nil || !info.IsDir() {
			return "", errs.Config("cli", "--cwd %q is not a directory", g.Cwd)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", errs.Internal("cli", "cannot determine the working directory: %v", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(wd); rerr == nil {
		return resolved, nil
	}
	return wd, nil
}

// Command registers a command with the app.
func (a *App) Command(cmd *Command) *App {
	a.commands = append(a.commands, cmd)
	a.byName[cmd.Name] = cmd
	for _, al := range cmd.Aliases {
		a.byName[al] = cmd
	}
	return a
}

// Lookup finds a command by name or alias.
func (a *App) Lookup(name string) *Command { return a.byName[name] }

// Commands returns registered commands sorted by name.
func (a *App) Commands() []*Command {
	out := append([]*Command(nil), a.commands...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Context carries everything a command needs to run.
type Context struct {
	App  *App
	Args []string
	// Flags is the command's flag set after parsing.
	Flags *flag.FlagSet
}

func (c *Context) String(name, def string) string {
	if v := c.Flags.Lookup(name); v != nil {
		if s, ok := v.Value.(interface{ String() string }); ok {
			return s.String()
		}
	}
	return def
}

func (c *Context) Bool(name string) bool {
	if v := c.Flags.Lookup(name); v != nil {
		return v.Value.String() == "true"
	}
	return false
}

func (c *Context) Int(name string, def int) int {
	if v := c.Flags.Lookup(name); v != nil {
		if n, err := strconv.Atoi(v.Value.String()); err == nil {
			return n
		}
	}
	return def
}

func (c *Context) StringSlice(name string) []string {
	if v := c.Flags.Lookup(name); v != nil {
		if s, ok := v.Value.(*stringSliceValue); ok {
			return append([]string(nil), s.items...)
		}
	}
	return nil
}

// StringSliceFlag registers a repeatable string flag and returns a pointer to
// its backing slice.
func StringSliceFlag(fs *flag.FlagSet, name, usage string) *[]string {
	v := &stringSliceValue{}
	fs.Var(v, name, usage)
	return &v.items
}

type stringSliceValue struct {
	items []string
}

func (v *stringSliceValue) String() string { return strings.Join(v.items, ",") }

func (v *stringSliceValue) Set(s string) error {
	v.items = append(v.items, s)
	return nil
}

// Run parses argv and dispatches. It returns the process exit code.
func (a *App) Run(argv []string) int {
	args := argv[1:]
	// A flag that the globals do not define but the session does (for example
	// --yes or --privacy) means the user wants an interactive session, not an
	// error: README documents `talon --yes "prompt"`.
	if len(args) > 0 && strings.HasPrefix(args[0], "-") && a.Lookup(args[0]) == nil &&
		a.globalSet.Lookup(strings.TrimLeft(strings.SplitN(args[0], "=", 2)[0], "-")) == nil {
		return a.runSession(args)
	}
	// Global flags may appear before the command name.
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if err := a.globalSet.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				a.printRootHelp()
				return 0
			}
			fmt.Fprintln(a.Stderr, errs.Usage("%v", err))
			return 2
		}
		args = a.globalSet.Args()
		if a.globals.Version {
			fmt.Fprintf(a.Stdout, "%s %s (%s)\n", app.Name, app.Version, app.Commit)
			return 0
		}
		if len(args) > 0 && strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(a.Stderr, "error: unknown flag %s\n", args[0])
			return 2
		}
		break
	}
	a.NoColor = a.NoColor || a.globals.NoColor

	if len(args) == 0 {
		return a.runDefault()
	}
	name := args[0]
	switch name {
	case "help", "--help", "-h":
		return a.runHelp(args[1:])
	}
	cmd := a.Lookup(name)
	if cmd == nil {
		// Allow bare "talon some prompt" to start a session with a prompt.
		return a.runSession(args)
	}
	return a.runCommand(cmd, args[1:])
}

// runCommand parses command flags and executes the command.
func (a *App) runCommand(cmd *Command, args []string) int {
	fs := flag.NewFlagSet("talon "+cmd.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if cmd.Setup != nil {
		cmd.Setup(fs)
	}
	if err := fs.Parse(permute(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.printCommandHelp(cmd)
			return 0
		}
		fmt.Fprintln(a.Stderr, errs.Usage("%v", err))
		return 2
	}
	ctx := &Context{App: a, Args: fs.Args(), Flags: fs}
	if err := cmd.Run(ctx, fs.Args()); err != nil {
		return a.reportError(err)
	}
	return 0
}

// permute moves flags ahead of positional arguments so that
// `talon config set model.name gpt-5 --project` works, not only
// `talon config set --project model.name gpt-5`. It knows which flags consume a
// value by inspecting the flag set.
func permute(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if eq := strings.Index(name, "="); eq >= 0 {
			// --name=value never consumes the next argument.
			flags = append(flags, arg)
			continue
		}
		flags = append(flags, arg)
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) {
			// The next argument is this flag's value.
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return append(flags, positional...)
}

// isBoolFlag reports whether a flag is a switch and therefore takes no value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// reportError prints err in a human-friendly way and maps it to an exit code.
func (a *App) reportError(err error) int {
	if err == nil {
		return 0
	}
	switch errs.KindOf(err) {
	case errs.KindUsage:
		fmt.Fprintf(a.Stderr, "error: %s\n", errs.User(err))
		return 2
	case errs.KindCancelled:
		return 130
	default:
		msg := errs.User(err)
		fmt.Fprintf(a.Stderr, "%s %s\n", symbol("error", a.NoColor), msg)
		return 1
	}
}

// runSession is defined by the interactive app; it is a field so that the
// agent layer can plug in without the CLI package importing it.
var sessionRunner func(a *App, args []string) int

// SetSessionRunner installs the interactive session entry point.
func SetSessionRunner(fn func(a *App, args []string) int) { sessionRunner = fn }

func (a *App) runSession(args []string) int {
	if sessionRunner == nil {
		fmt.Fprintln(a.Stderr, "error: interactive mode is unavailable in this build")
		return 1
	}
	return sessionRunner(a, args)
}

// runDefault starts an empty interactive session.
func (a *App) runDefault() int { return a.runSession(nil) }

func (a *App) runHelp(args []string) int {
	if len(args) > 0 {
		cmd := a.Lookup(args[0])
		if cmd == nil {
			fmt.Fprintf(a.Stderr, "error: unknown command %q\n", args[0])
			return 2
		}
		a.printCommandHelp(cmd)
		return 0
	}
	a.printRootHelp()
	return 0
}

func (a *App) printRootHelp() {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s — agentic coding CLI\n\n", app.Name, app.Version)
	b.WriteString("USAGE\n  talon [flags] [command] [args]\n\n")
	b.WriteString("Running talon with no command starts an interactive session.\n")
	b.WriteString("Running talon with no command but a prompt runs it once and exits:\n")
	b.WriteString("  talon \"fix the failing tests\"\n\n")
	b.WriteString("COMMANDS\n")
	width := 0
	for _, c := range a.Commands() {
		if c.Hidden {
			continue
		}
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range a.Commands() {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, c.Name, c.Summary)
	}
	b.WriteString("\nGLOBAL FLAGS\n")
	fmt.Fprintf(&b, "  %s\n", flagUsages(a.globalSet))
	b.WriteString("\nENVIRONMENT\n")
	b.WriteString("  AI_API_KEY, AI_BASE_URL, AI_MODEL, AI_PROVIDER   provider settings\n")
	b.WriteString("  TALON_PERMISSIONS, TALON_THEME, TALON_LOG_LEVEL   UI and permissions\n")
	fmt.Fprint(a.Stdout, b.String())
}

func (a *App) printCommandHelp(cmd *Command) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", cmd.Name, cmd.Summary)
	usage := cmd.Usage
	if usage == "" {
		usage = "talon " + cmd.Name + " [flags] [args]"
	}
	fmt.Fprintf(&b, "USAGE\n  %s\n", usage)
	if cmd.Detailed != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimRight(cmd.Detailed, "\n"))
	}
	if cmd.Setup != nil {
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		cmd.Setup(fs)
		if usages := strings.TrimSpace(flagUsages(fs)); usages != "" {
			fmt.Fprintf(&b, "\nFLAGS\n%s\n", usages)
		}
	}
	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(&b, "\nALIASES\n  %s\n", strings.Join(cmd.Aliases, ", "))
	}
	fmt.Fprint(a.Stdout, b.String())
}

// flagUsages renders a flag set's usage block into a string.
func flagUsages(fs *flag.FlagSet) string {
	var buf strings.Builder
	prev := fs.Output()
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(prev)
	return buf.String()
}

func symbol(kind string, noColor bool) string {
	if noColor {
		return "error:"
	}
	switch kind {
	case "error":
		return "\x1b[31merror:\x1b[0m"
	default:
		return kind + ":"
	}
}
