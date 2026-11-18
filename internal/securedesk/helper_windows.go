//go:build windows

package securedesk

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/actions/remote"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
)

// RunHelper serves the pipe of this session until the process is ended by
// the service. ffmpeg is the encoder's path, fixed by the service.
func RunHelper(ffmpeg string) int {
	logTo("desktop-helper.log")
	session.InitDPIAwareness()
	var sid uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid); err != nil {
		log.Printf("desktop helper: %v", err)
		return 1
	}
	// SYSTEM, and the user logged on to this session: vm-agent runs as that
	// user. Without one (the logon screen) only SYSTEM may connect.
	sd := "D:P(A;;GA;;;SY)"
	var user windows.Token
	if err := windows.WTSQueryUserToken(sid, &user); err == nil {
		if tu, err := user.GetTokenUser(); err == nil {
			sd += "(A;;GRGW;;;" + tu.User.Sid.String() + ")"
		}
		user.Close()
	}
	l, err := winio.ListenPipe(PipePath(sid), &winio.PipeConfig{SecurityDescriptor: sd, InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10})
	if err != nil {
		log.Printf("desktop helper: %v", err)
		return 1
	}
	h := &helper{ffmpeg: ffmpeg, session: sid, work: make(chan func(*remote.Injector), 256)}
	locked, known := win.SessionLocked(sid)
	log.Printf("desktop helper: session %d locked=%v (known=%v)", sid, locked, known)
	go h.inputThread()
	log.Printf("desktop helper: serving session %d", sid)
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("desktop helper: %v", err)
			return 1
		}
		go h.serve(conn)
	}
}

type helper struct {
	ffmpeg  string
	session uint32
	work    chan func(*remote.Injector)

	mu      sync.Mutex
	checked time.Time
	allowed bool
	refused time.Time
}

// lockScreen reports whether the helper may act: only while the session is
// locked and no elevation prompt is up. The prompt uses the same desktop as
// the lock screen, and a helper that operated it would let any program of
// the user answer it, which is what the prompt exists to prevent. On the
// lock screen input is harmless without the password. An unreadable state
// counts as not locked.
func (h *helper) lockScreen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.checked) < 250*time.Millisecond {
		return h.allowed
	}
	locked, known := win.SessionLocked(h.session)
	h.allowed = known && locked && !win.ProcessInSession("consent.exe", h.session)
	h.checked = time.Now()
	return h.allowed
}

// refuse logs a refusal, at most once every ten seconds.
func (h *helper) refuse(what string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.refused) > 10*time.Second {
		h.refused = time.Now()
		log.Printf("desktop helper: refused %s: the session is not on the lock screen", what)
	}
}

var errNotLockScreen = errors.New("the session is not on the lock screen")

// inputThread applies input on one OS thread, which is moved to whichever
// desktop is receiving input before every message.
func (h *helper) inputThread() {
	runtime.LockOSThread()
	inj := remote.NewInjector(input.NewBackend())
	for f := range h.work {
		if err := win.AttachInputDesktop(); err != nil {
			log.Printf("desktop helper: attach to the input desktop: %v", err)
		}
		f(inj)
	}
}

func (h *helper) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 64<<10)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var req request
	if json.Unmarshal(line, &req) != nil {
		return
	}
	switch req.Op {
	case "input":
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 1 && !h.lockScreen() {
				h.refuse("input")
			} else if len(line) > 1 {
				msg := append([]byte(nil), line...)
				h.work <- func(inj *remote.Injector) {
					if err := inj.Apply(msg); err != nil {
						log.Printf("desktop helper: input: %v", err)
					}
				}
			}
			if err != nil {
				// The client went away: nothing may stay held down.
				h.work <- func(inj *remote.Injector) { inj.Release() }
				return
			}
		}
	case "video":
		if err := h.video(conn, br, req); err != nil {
			reply, _ := json.Marshal(request{Error: err.Error()})
			_, _ = conn.Write(append(reply, '\n'))
		}
	}
}

// video captures this process's desktop, the Winlogon desktop the service
// started it on, for as long as the connection stays open. Every value that
// reaches the command line is a number checked here.
func (h *helper) video(conn net.Conn, br *bufio.Reader, r request) error {
	switch {
	case !h.lockScreen():
		h.refuse("video")
		return errNotLockScreen
	case h.ffmpeg == "":
		return fmt.Errorf("the service was installed without an ffmpeg path")
	case r.FPS < 1 || r.FPS > 60, r.Kbps < 100 || r.Kbps > 50000, r.Port < 1024 || r.Port > 65535,
		r.W < 16 || r.W > 16384, r.H < 16 || r.H > 16384:
		return fmt.Errorf("video parameters out of range")
	}
	kbps := strconv.Itoa(r.Kbps) + "k"
	cmd := exec.Command(h.ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		// The pointer is drawn in: its shape cannot be read from outside
		// this desktop.
		"-f", "gdigrab", "-framerate", strconv.Itoa(r.FPS), "-draw_mouse", "1", "-offset_x", "0", "-offset_y", "0",
		"-video_size", fmt.Sprintf("%dx%d", r.W&^1, r.H&^1), "-i", "desktop", "-fps_mode", "passthrough",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p", "-g", strconv.Itoa(r.FPS*2),
		"-b:v", kbps, "-maxrate", kbps, "-bufsize", strconv.Itoa(r.Kbps/2)+"k",
		"-bsf:v", "dump_extra=freq=keyframe", "-f", "rtp", "rtp://127.0.0.1:"+strconv.Itoa(r.Port)+"?pkt_size=1200")
	cmd.Env = append(os.Environ(), "__COMPAT_LAYER=HighDpiAware")
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, br)
		close(closed)
	}()
	// The capture ends as soon as the lock screen is left.
	left := make(chan struct{})
	go func() {
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-closed:
				return
			case <-t.C:
				if !h.lockScreen() {
					close(left)
					return
				}
			}
		}
	}()
	select {
	case <-closed:
		_ = cmd.Process.Kill()
		<-exited
		return nil
	case <-left:
		_ = cmd.Process.Kill()
		<-exited
		return errNotLockScreen
	case err := <-exited:
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return fmt.Errorf("ffmpeg ended: %v %s", err, msg)
	}
}
