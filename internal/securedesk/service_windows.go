//go:build windows

package securedesk

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// RunService runs the Windows service: it keeps one helper alive in the
// console session and does nothing else.
func RunService(ffmpeg string) int {
	logTo("desktop-service.log")
	if err := svc.Run(ServiceName, &service{ffmpeg: ffmpeg}); err != nil {
		log.Printf("desktop service: %v", err)
		return 1
	}
	return 0
}

// logTo sends the log to a file under %ProgramData%\vm-agent: neither the
// service nor the helper has a console.
func logTo(name string) {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, "vm-agent")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(path, path+".1")
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}
}

type service struct {
	ffmpeg  string
	process windows.Handle
	session uint32
}

const noSession = 0xFFFFFFFF

func (s *service) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	s.session = noSession
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Printf("desktop service: started")
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	failures := 0
	for {
		select {
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				s.stopHelper()
				log.Printf("desktop service: stopped")
				return false, 0
			}
		case <-tick.C:
			active := windows.WTSGetActiveConsoleSessionId()
			if s.process != 0 && (active != s.session || !running(s.process)) {
				s.stopHelper()
			}
			if s.process == 0 && active != noSession && failures < 30 {
				if err := s.startHelper(active); err != nil {
					// Logged a few times, not every second.
					if failures++; failures <= 3 {
						log.Printf("desktop service: start the helper in session %d: %v", active, err)
					}
				} else {
					failures = 0
					log.Printf("desktop service: helper started in session %d", active)
				}
			}
		}
	}
}

func running(p windows.Handle) bool {
	ev, err := windows.WaitForSingleObject(p, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

func (s *service) stopHelper() {
	if s.process == 0 {
		return
	}
	_ = windows.TerminateProcess(s.process, 0)
	_, _ = windows.WaitForSingleObject(s.process, 3000)
	_ = windows.CloseHandle(s.process)
	s.process, s.session = 0, noSession
}

// startHelper runs this executable as SYSTEM inside the session, on the
// Winlogon desktop: a copy of the service's own token is moved to the
// session, which a service is allowed to do.
func (s *service) startHelper(session uint32) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &self); err != nil {
		return fmt.Errorf("open the service token: %w", err)
	}
	defer self.Close()
	var token windows.Token
	if err := windows.DuplicateTokenEx(self, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &token); err != nil {
		return fmt.Errorf("duplicate the token: %w", err)
	}
	defer token.Close()
	if err := windows.SetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), 4); err != nil {
		return fmt.Errorf("move the token to the session: %w", err)
	}
	cmdline := windows.EscapeArg(exe) + " -desktop-helper"
	if s.ffmpeg != "" {
		cmdline += " -ffmpeg " + windows.EscapeArg(s.ffmpeg)
	}
	cmd, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\Winlogon`)
	si := windows.StartupInfo{Desktop: desktop}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	const createNoWindow = 0x08000000
	if err := windows.CreateProcessAsUser(token, nil, cmd, nil, nil, false, createNoWindow, nil, nil, &si, &pi); err != nil {
		return fmt.Errorf("create the helper process: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)
	s.process, s.session = pi.Process, session
	return nil
}
