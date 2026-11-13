//go:build unix

package agentbrowser

import (
	"os/exec"
	"syscall"
)

// configureProcess puts the CLI in its own process group so cancellation kills
// the CLI and any helper it started, without touching the harness.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
