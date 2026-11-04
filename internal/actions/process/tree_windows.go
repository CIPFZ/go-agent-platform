//go:build windows

package process

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type treeSys struct {
	job windows.Handle
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// The root process starts suspended so it is inside the Job before it can run
// a single instruction; otherwise a fast child could spawn descendants that
// escape the Job. The handle is closed only after the root exits, and closing
// it terminates whatever is left in the tree.
func (t *Tree) start() error {
	job, err := newKillOnCloseJob()
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}
	t.sys.job = job
	if t.cmd.SysProcAttr == nil {
		t.cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	t.cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	HideConsole(t.cmd)
	if t.cmd.WaitDelay == 0 {
		t.cmd.WaitDelay = time.Second
	}
	if err := t.cmd.Start(); err != nil {
		t.release()
		return err
	}
	pid := uint32(t.cmd.Process.Pid)
	if err := assignToJob(job, pid); err != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
		t.release()
		return fmt.Errorf("assign process to job: %w", err)
	}
	if err := resumeProcess(pid); err != nil {
		_ = t.kill()
		_ = t.cmd.Wait()
		t.release()
		return fmt.Errorf("resume process: %w", err)
	}
	return nil
}

func assignToJob(job windows.Handle, pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.AssignProcessToJobObject(job, handle)
}

// resumeProcess resumes every thread of a process created suspended. os/exec
// does not expose the primary thread handle, so the threads are found with a
// toolhelp snapshot.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	resumed := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return resumeErr
		}
		resumed++
	}
	if resumed == 0 {
		return errors.New("no thread found for suspended process")
	}
	return nil
}

func (t *Tree) kill() error {
	if t.sys.job == 0 {
		return os.ErrProcessDone
	}
	if err := windows.TerminateJobObject(t.sys.job, 1); err != nil {
		if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (t *Tree) release() {
	if t.sys.job != 0 {
		_ = windows.CloseHandle(t.sys.job)
		t.sys.job = 0
	}
}
