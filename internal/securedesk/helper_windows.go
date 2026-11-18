//go:build windows

package securedesk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

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
	h := &helper{ffmpeg: ffmpeg, work: make(chan func(*remote.Injector), 256)}
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
	ffmpeg string
	work   chan func(*remote.Injector)
}

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
			if len(line) > 1 {
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
	select {
	case <-closed:
		_ = cmd.Process.Kill()
		<-exited
		return nil
	case err := <-exited:
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return fmt.Errorf("ffmpeg ended: %v %s", err, msg)
	}
}
