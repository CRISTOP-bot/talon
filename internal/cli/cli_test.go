package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/errs"
)

// testApp builds an app with a couple of commands writing into a buffer.
func testApp(out, errOut *bytes.Buffer) (*App, string) {
	a := NewApp()
	a.Stdout = out
	a.Stderr = errOut
	seen := ""
	a.Command(&Command{
		Name:    "hello",
		Aliases: []string{"hi"},
		Summary: "say hello",
		Setup: func(fs *flag.FlagSet) {
			fs.String("name", "world", "who to greet")
			fs.Int("times", 1, "how many times")
		},
		Run: func(c *Context, args []string) error {
			seen = c.String("name", "world")
			name := seen
			for i := 0; i < c.Int("times", 1); i++ {
				fmt.Fprintln(c.App.Stdout, "hello", name)
			}
			return nil
		},
	})
	a.Command(&Command{
		Name:    "boom",
		Summary: "always fails",
		Setup:   func(fs *flag.FlagSet) {},
		Run: func(c *Context, args []string) error {
			return errs.Usage("this command is broken")
		},
	})
	a.Command(&Command{
		Name:    "fatal",
		Summary: "fails in an unexpected way",
		Setup:   func(fs *flag.FlagSet) {},
		Run: func(c *Context, args []string) error {
			return errors.New("boom")
		},
	})
	a.Command(&Command{
		Name:    "secret",
		Summary: "hidden command",
		Hidden:  true,
		Setup:   func(fs *flag.FlagSet) {},
		Run:     func(c *Context, args []string) error { return nil },
	})
	a.Command(&Command{
		Name:    "prompt",
		Summary: "reads a prompt",
		Setup:   func(fs *flag.FlagSet) {},
		Run:     func(c *Context, args []string) error { return nil },
	})
	return a, seen
}

func TestRunCommandWithFlags(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	code := a.Run([]string{"talon", "hello", "--name", "talon", "--times", "2"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	if got := out.String(); got != "hello talon\nhello talon\n" {
		t.Errorf("output = %q", got)
	}
}

func TestAliasDispatch(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "hi"}); code != 0 {
		t.Fatalf("alias failed: %d", code)
	}
	if !strings.Contains(out.String(), "hello world") {
		t.Errorf("output = %q", out.String())
	}
}

func TestUnknownCommandStartsASession(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	var got []string
	SetSessionRunner(func(app *App, args []string) int {
		got = args
		return 0
	})
	defer SetSessionRunner(nil)

	if code := a.Run([]string{"talon", "fix", "the", "tests"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Join(got, " ") != "fix the tests" {
		t.Errorf("session args = %v", got)
	}
}

func TestNoArgumentsStartsASession(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	called := false
	SetSessionRunner(func(app *App, args []string) int {
		called = true
		return 0
	})
	defer SetSessionRunner(nil)

	a.Run([]string{"talon"})
	if !called {
		t.Error("the interactive session should start with no arguments")
	}
}

func TestUsageErrorExitCode(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "boom"}); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "broken") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestUnknownErrorExitCode(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "fatal"}); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestBadFlagExitCode(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "hello", "--nope"}); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestGlobalFlagsBeforeCommand(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	code := a.Run([]string{"talon", "--no-color", "--cwd", "/tmp", "hello"})
	if code != 0 {
		t.Fatalf("exit code = %d (%s)", code, errOut.String())
	}
	if !a.Globals().NoColor {
		t.Error("--no-color was not applied")
	}
	if a.Globals().Cwd != "/tmp" {
		t.Errorf("--cwd = %q", a.Globals().Cwd)
	}
}

func TestVersionFlag(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a := NewApp()
	a.Stdout = out
	a.Stderr = errOut
	if code := a.Run([]string{"talon", "--version"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "talon") {
		t.Errorf("output = %q", out.String())
	}
}

func TestHelpListsCommandsAndHidesHidden(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "help"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	text := out.String()
	for _, want := range []string{"hello", "boom", "COMMANDS", "GLOBAL FLAGS", "ENVIRONMENT"} {
		if !strings.Contains(text, want) {
			t.Errorf("help is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret") {
		t.Errorf("hidden commands must not be listed:\n%s", text)
	}
}

func TestCommandHelp(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "help", "hello"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	text := out.String()
	if !strings.Contains(text, "-name") || !strings.Contains(text, "say hello") {
		t.Errorf("command help = %s", text)
	}
	if code := a.Run([]string{"talon", "help", "nope"}); code != 2 {
		t.Errorf("unknown command help exit code = %d", code)
	}
}

func TestFlagHelpForCommand(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a, _ := testApp(out, errOut)
	if code := a.Run([]string{"talon", "hello", "-h"}); code != 0 {
		t.Errorf("exit code = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "-times") {
		t.Errorf("flag help = %s", out.String())
	}
}

func TestWorkingDirResolution(t *testing.T) {
	a := NewApp()
	a.globals.Cwd = t.TempDir()
	dir, err := a.Globals().WorkingDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Error("empty working dir")
	}
	a.globals.Cwd = "/definitely/not/here"
	if _, err := a.Globals().WorkingDir(); err == nil {
		t.Error("a missing --cwd should error")
	}
}

func TestStringSliceFlag(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	items := StringSliceFlag(fs, "tag", "repeatable")
	c := &Context{Flags: fs}
	if err := fs.Parse([]string{"--tag", "a", "--tag", "b"}); err != nil {
		t.Fatal(err)
	}
	got := c.StringSlice("tag")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("tags = %v (raw %v)", got, *items)
	}
}
