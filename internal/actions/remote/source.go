package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Params describe the video stream of one session.
type Params struct {
	FPS         int
	BitrateKbps int
	// Display is the captured monitor in API coordinates.
	Display protocol.Rect
}

// Status is what the source is currently doing.
type Status struct {
	// Capture is "dda" (Desktop Duplication) or "gdi".
	Capture string `json:"capture"`
	Encoder string `json:"encoder"`
}

// Source produces the RTP video stream of a session. Run blocks until ctx is
// done or the source cannot continue, calling emit for every packet (from one
// goroutine) and status whenever the pipeline changes.
type Source interface {
	Available() error
	Run(ctx context.Context, p Params, emit func(*rtp.Packet), status func(Status)) error
}

// candidate is one capture and encoder combination, in order of preference.
type candidate struct {
	name    string
	capture string
	encoder string
	// live candidates need a display the compositor is presenting to.
	live bool
	// needs are the ffmpeg encoder and filter names the candidate uses.
	needs []string
	args  func(p Params) []string
	env   func(env []string) []string
}

// Timing of the ffmpeg pipeline supervision.
const (
	firstPacketTimeout = 6 * time.Second
	stallTimeout       = 3 * time.Second
	candidateRetry     = time.Minute
	displayCheckPeriod = 2 * time.Second
	maxSourceFailures  = 5
)

// ffmpegSource runs ffmpeg with the first working candidate and forwards the
// RTP packets it sends to a loopback socket.
type ffmpegSource struct {
	ffmpeg        string
	candidates    []candidate
	displayActive func() bool

	mu      sync.Mutex
	probed  bool
	support map[string]bool
	bad     map[string]time.Time
}

func newFFmpegSource(ffmpeg string) *ffmpegSource {
	return &ffmpegSource{ffmpeg: ffmpeg, candidates: platformCandidates(), displayActive: platformDisplayActive, bad: map[string]time.Time{}}
}

func (s *ffmpegSource) Available() error {
	if len(s.candidates) == 0 {
		return protocol.Unsupported("the remote desktop is not supported on this platform")
	}
	if s.ffmpeg == "" {
		return protocol.Unsupported("the remote desktop needs ffmpeg, which was not found")
	}
	return nil
}

// probe asks ffmpeg once which encoders and filters it has, so candidates it
// cannot run are skipped without starting them.
func (s *ffmpegSource) probe(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probed {
		return
	}
	s.probed = true
	s.support = map[string]bool{}
	for _, arg := range []string{"-encoders", "-filters", "-devices"} {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(ctx, s.ffmpeg, "-hide_banner", arg)
		hideWindow(cmd)
		out, err := cmd.Output()
		cancel()
		if err != nil {
			// Unknown support: let every candidate try.
			s.support = nil
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 {
				s.support[f[1]] = true
			}
		}
	}
}

func (s *ffmpegSource) usable(c candidate, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.bad[c.name]; ok && now.Before(until) {
		return false
	}
	if s.support != nil {
		for _, need := range c.needs {
			if !s.support[need] {
				return false
			}
		}
	}
	return true
}

// pick returns the preferred usable candidate for the display state: live
// candidates only while a display is active, and the others as fallback.
func (s *ffmpegSource) pick(active bool) *candidate {
	now := time.Now()
	for pass := 0; pass < 2; pass++ {
		for i := range s.candidates {
			c := &s.candidates[i]
			if c.live == (pass == 0) && (active || !c.live) && s.usable(*c, now) {
				return c
			}
		}
	}
	return nil
}

func (s *ffmpegSource) markBad(c *candidate) {
	s.mu.Lock()
	s.bad[c.name] = time.Now().Add(candidateRetry)
	s.mu.Unlock()
}

type outcome int

const (
	outcomeStopped  outcome = iota // ctx is done
	outcomeNoStart                 // no packet ever arrived
	outcomeFailed                  // ran, then exited or stalled
	outcomeSwitched                // the display state changed
)

func (s *ffmpegSource) Run(ctx context.Context, p Params, emit func(*rtp.Packet), status func(Status)) error {
	if err := s.Available(); err != nil {
		return err
	}
	s.probe(ctx)
	failures := 0
	var lastErr error
	for ctx.Err() == nil {
		active := s.displayActive()
		c := s.pick(active)
		if c == nil {
			if lastErr == nil {
				lastErr = errors.New("ffmpeg has no usable capture and H.264 encoder combination")
			}
			return protocol.Errorf(protocol.CodeIOFailed, "no working capture pipeline: %v", lastErr)
		}
		out, err := s.runOne(ctx, c, p, emit, status)
		switch out {
		case outcomeStopped:
			return nil
		case outcomeSwitched:
			failures = 0
		case outcomeNoStart:
			log.Printf("vm-agent: remote: %s did not start: %v", c.name, err)
			lastErr = fmt.Errorf("%s: %w", c.name, err)
			s.markBad(c)
		case outcomeFailed:
			log.Printf("vm-agent: remote: %s stopped: %v", c.name, err)
			lastErr = fmt.Errorf("%s: %w", c.name, err)
			if failures++; failures >= maxSourceFailures {
				return protocol.Errorf(protocol.CodeIOFailed, "the capture pipeline keeps failing: %v", lastErr)
			}
			select {
			case <-ctx.Done():
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	return nil
}

// listenRTP binds an even loopback port for RTP and the next one for the
// RTCP that ffmpeg's RTP muxer sends alongside.
func listenRTP() (rtpConn, rtcpConn *net.UDPConn, err error) {
	loopback := net.IPv4(127, 0, 0, 1)
	for i := 0; i < 64; i++ {
		c, lerr := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback})
		if lerr != nil {
			return nil, nil, lerr
		}
		port := c.LocalAddr().(*net.UDPAddr).Port
		if port%2 == 0 && port < 65535 {
			if c2, lerr := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback, Port: port + 1}); lerr == nil {
				return c, c2, nil
			}
		}
		c.Close()
	}
	return nil, nil, errors.New("no free loopback port pair for RTP")
}

func (s *ffmpegSource) runOne(ctx context.Context, c *candidate, p Params, emit func(*rtp.Packet), status func(Status)) (outcome, error) {
	conn, rtcpConn, err := listenRTP()
	if err != nil {
		return outcomeNoStart, err
	}
	defer conn.Close()
	defer rtcpConn.Close()
	_ = conn.SetReadBuffer(4 << 20)
	port := conn.LocalAddr().(*net.UDPAddr).Port

	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, c.args(p)...)
	// Parameter sets travel with every keyframe so a viewer can start
	// decoding at any of them.
	args = append(args, "-bsf:v", "dump_extra=freq=keyframe", "-f", "rtp", "rtp://127.0.0.1:"+strconv.Itoa(port)+"?pkt_size=1200")
	cmd := exec.Command(s.ffmpeg, args...)
	cmd.Env = os.Environ()
	if c.env != nil {
		cmd.Env = c.env(cmd.Env)
	}
	stderr := &tail{max: 4 << 10}
	cmd.Stderr = stderr
	tree, err := process.StartTree(cmd)
	if err != nil {
		return outcomeNoStart, err
	}
	exited := make(chan error, 1)
	go func() { exited <- tree.Wait() }()
	stop := func() {
		_ = tree.Kill()
		<-exited
	}

	// The display state is sampled off the packet path: the check blocks for
	// several compositor intervals.
	var mismatch atomic.Bool
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() {
		t := time.NewTicker(displayCheckPeriod)
		defer t.Stop()
		differ := 0
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-t.C:
				if s.displayActive() != c.live {
					if differ++; differ >= 2 {
						mismatch.Store(true)
					}
				} else {
					differ = 0
				}
			}
		}
	}()

	started := time.Now()
	var first, last time.Time
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			stop()
			return outcomeStopped, nil
		case werr := <-exited:
			err := fmt.Errorf("ffmpeg exited: %s", firstLine(stderr.String(), werr))
			if first.IsZero() {
				return outcomeNoStart, err
			}
			return outcomeFailed, err
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, rerr := conn.Read(buf)
		now := time.Now()
		if rerr == nil && n > 12 {
			pkt := &rtp.Packet{}
			if pkt.Unmarshal(append([]byte(nil), buf[:n]...)) == nil {
				if first.IsZero() {
					first = now
					status(Status{Capture: c.capture, Encoder: c.encoder})
				}
				last = now
				emit(pkt)
			}
		} else if rerr != nil {
			var nerr net.Error
			if !errors.As(rerr, &nerr) || !nerr.Timeout() {
				stop()
				return outcomeFailed, rerr
			}
		}
		switch {
		case first.IsZero() && now.Sub(started) > firstPacketTimeout:
			stop()
			return outcomeNoStart, fmt.Errorf("no video within %s: %s", firstPacketTimeout, firstLine(stderr.String(), nil))
		case !first.IsZero() && now.Sub(last) > stallTimeout:
			stop()
			return outcomeFailed, fmt.Errorf("video stalled for %s: %s", stallTimeout, firstLine(stderr.String(), nil))
		case !first.IsZero() && mismatch.Load():
			stop()
			return outcomeSwitched, nil
		}
	}
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func firstLine(msg string, err error) string {
	for _, line := range strings.Split(msg, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "no diagnostic output"
}
