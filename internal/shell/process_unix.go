//go:build unix

package shell

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the command in its own process group so a timeout kills
// the whole tree, not just the shell.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree terminates the command and every child it spawned.
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative pid targets the process group created above.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
