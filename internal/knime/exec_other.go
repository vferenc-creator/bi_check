//go:build !windows

package knime

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// defaultCommand starts KNIME in its own process group; cancelling kills the
// whole group.
func defaultCommand(ctx context.Context, exe string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 15 * time.Second
	return cmd
}

// killWithParent: on Linux the process group is killed on cancel; nothing
// more to do.
func killWithParent(cmd *exec.Cmd) func() { return func() {} }
