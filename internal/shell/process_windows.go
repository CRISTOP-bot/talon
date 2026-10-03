//go:build windows

package shell

import (
	"os/exec"
	"syscall"
)

// configureProcess creates a new process group so Ctrl+C handling and timeouts
// behave predictably on Windows.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killTree terminates the command. Windows has no cheap recursive kill, so the
// direct child is targeted; child processes are expected to exit with it.
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
