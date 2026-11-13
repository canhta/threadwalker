//go:build !unix

package agentbrowser

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(*exec.Cmd) {}

func killGroup(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func killPID(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
