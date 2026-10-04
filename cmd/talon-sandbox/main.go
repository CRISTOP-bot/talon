// Command talon-sandbox runs any command under the same kernel confinement Talon
// applies to the commands it runs itself.
//
// It exists for two reasons: to try the policy by hand before trusting it with a
// real agent, and to give CI a way to run untrusted steps confined by the kernel.
//
//	talon-sandbox -- make test
//	talon-sandbox --root ./src --net-port 443 -- git fetch
//	talon-sandbox status
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/sandbox"
	"github.com/CRISTOP-bot/talon/internal/secrets"
)

// stringList collects a flag that may be repeated.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// uintList collects numeric flags that may be repeated.
type uintList []uint64

func (u *uintList) String() string { return "" }

func (u *uintList) Set(v string) error {
	n, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		return fmt.Errorf("expected a port number, got %q", v)
	}
	*u = append(*u, n)
	return nil
}

func main() {
	// This binary re-executes itself to apply Landlock, so it must recognise its
	// own helper invocation before parsing flags.
	if sandbox.IsHelperInvocation(os.Args[1:]) {
		os.Exit(sandbox.RunHelper(os.Args[1:]))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	// "status" is a subcommand, not a flag, so it is handled before parsing.
	if len(args) > 0 && args[0] == "status" {
		fmt.Fprint(stdout, sandbox.Detect().String())
		return 0
	}
	fs := flag.NewFlagSet("talon-sandbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var roots, writeRoots, readOnlyRoots stringList
	var ports uintList
	var keepEnv bool
	statusOnly := false
	fs.Var(&roots, "root", "directory the command may read and write (repeatable)")
	fs.Var(&writeRoots, "write-root", "extra directory the command may write (repeatable)")
	fs.Var(&readOnlyRoots, "read-root", "extra directory the command may read (repeatable)")
	fs.Var(&ports, "net-port", "TCP port the command may connect to (repeatable)")
	fs.BoolVar(&keepEnv, "keep-env", false, "pass the environment unchanged instead of removing credentials")
	fs.Usage = func() {
		fmt.Fprint(stderr, `talon-sandbox runs a command under Talon's kernel sandbox.

Usage:
  talon-sandbox [flags] -- command [args...]
  talon-sandbox status

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Examples:
  talon-sandbox -- make test
  talon-sandbox --root . --net-port 443 -- git fetch
  talon-sandbox --read-root ./fixtures -- ./script.sh

Without --keep-env the child receives the environment with credentials removed.
Exit status is the command's own, or 125 when the sandbox itself failed.

Note on /dev: Landlock cannot express a rule for a character device, so /dev/null
is not granted by default and "cmd > /dev/null" fails with "permission denied".
Use --write-root /dev when a build needs it.
`)
	}
	if err := fs.Parse(args); err != nil {
		return 125
	}

	if statusOnly {
		fmt.Fprint(stdout, sandbox.Detect().String())
		return 0
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "error: no command given")
		fs.Usage()
		return 125
	}

	policy, err := buildPolicy(roots, writeRoots, readOnlyRoots, ports, keepEnv)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 125
	}

	name, err := exec.LookPath(rest[0])
	if err != nil {
		fmt.Fprintf(stderr, "error: %s: %v\n", rest[0], err)
		return 127
	}
	policy.ExecPaths = append(policy.ExecPaths, name)

	cmd, isolated, err := sandbox.Command(policy, name, rest[1:])
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot prepare the sandbox: %v\n", err)
		return 125
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr

	if !isolated {
		fmt.Fprintln(stderr,
			"warning: no kernel sandbox is available here; only Talon's path and environment policy applies")
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 125
	}
	return 0
}

// buildPolicy turns the flags into a sandbox policy.
func buildPolicy(roots, writeRoots, readOnlyRoots stringList, ports uintList, keepEnv bool) (sandbox.Policy, error) {
	workdir, err := os.Getwd()
	if err != nil {
		return sandbox.Policy{}, err
	}
	base := []string{workdir}
	for _, r := range roots {
		abs, aerr := filepath.Abs(r)
		if aerr != nil {
			return sandbox.Policy{}, aerr
		}
		if _, serr := os.Stat(abs); serr != nil {
			return sandbox.Policy{}, fmt.Errorf("%s: %v", abs, serr)
		}
		base = append(base, abs)
	}
	env := os.Environ()
	if !keepEnv {
		env = secrets.FilterEnv(env, nil, false)
	}
	policy := sandbox.DefaultPolicy(workdir, env)
	policy.ReadRoots = append(policy.ReadRoots, base...)
	for _, w := range writeRoots {
		abs, aerr := filepath.Abs(w)
		if aerr != nil {
			return sandbox.Policy{}, aerr
		}
		policy.WriteRoots = append(policy.WriteRoots, abs)
	}
	for _, r := range readOnlyRoots {
		abs, aerr := filepath.Abs(r)
		if aerr != nil {
			return sandbox.Policy{}, aerr
		}
		policy.ReadRoots = append(policy.ReadRoots, abs)
	}
	if len(ports) > 0 {
		policy.Network = sandbox.NetworkPorts
		for _, p := range ports {
			policy.AllowedPorts = append(policy.AllowedPorts, int(p))
		}
	}
	if err := policy.Validate(); err != nil {
		return sandbox.Policy{}, err
	}
	return policy, nil
}

// asExitError reports whether err is an *exec.ExitError and assigns it.
func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
