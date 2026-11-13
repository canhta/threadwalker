//go:build unix

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func freeDiskBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// runJournalctl streams the service's records from journald.
func runJournalctl(ctx context.Context, args []string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	return &journalReader{out: out, cmd: cmd}, nil
}

type journalReader struct {
	out io.ReadCloser
	cmd *exec.Cmd
}

func (j *journalReader) Read(p []byte) (int, error) { return j.out.Read(p) }
func (j *journalReader) Close() error {
	_ = j.out.Close()
	_ = j.cmd.Process.Kill()
	return j.cmd.Wait()
}

// ownerAttr runs a child as the owner of path when the caller is root, so the
// check and doctor never create root-owned database files beside the service's.
func ownerAttr(path string) *syscall.SysProcAttr {
	var st syscall.Stat_t
	if os.Geteuid() != 0 || syscall.Stat(path, &st) != nil || st.Uid == 0 {
		return nil
	}
	return &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid}}
}
