//go:build !linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
)

// HelperFlag is the internal flag Talon re-executes itself with.
const HelperFlag = "--talon-sandbox-exec"

// PolicyFlag is the internal flag carrying the sandbox policy path.
const PolicyFlag = "--talon-sandbox-policy"

// PolicyEnv names the environment variable that carries the policy path.
const PolicyEnv = "TALON_SANDBOX_POLICY"

// probeLandlock returns 0 on platforms without Landlock.
func probeLandlock() int { return 0 }

// helperCommand reports that no kernel helper exists on this platform. Talon
// falls back to its in-process policy and says so explicitly rather than
// pretending the process is isolated.
func helperCommand(policy Policy, name string, args []string) (*exec.Cmd, bool, error) {
	return nil, false, nil
}

// RunHelper is never reached on platforms without kernel support; it exists so
// the command line stays uniform.
func RunHelper(argv []string) int {
	fmt.Fprintln(os.Stderr, "talon sandbox helper: this platform has no kernel sandbox support")
	return 2
}
