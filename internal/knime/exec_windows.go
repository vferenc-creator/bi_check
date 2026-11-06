//go:build windows

package knime

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNoWindow = 0x08000000

// defaultCommand starts KNIME without a window; cancelling kills the whole
// process tree (the launcher and the Java VM).
func defaultCommand(ctx context.Context, exe string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	return cmd
}

// killWithParent puts the started KNIME into a Windows job object that kills
// its whole process tree when the job handle closes – also if the BI
// Monitor itself ends (closed, crashed or killed). The returned function
// closes the handle after the run.
func killWithParent(cmd *exec.Cmd) func() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() {}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return func() {}
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return func() {}
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return func() {}
	}
	return func() { windows.CloseHandle(job) }
}
