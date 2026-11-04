// Package record implements /v1/record (spec 6.8): one ffmpeg screen
// recording at a time, stored as fragmented MP4 under -record-dir, with
// quota and retention cleanup.
package record

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Session states.
const (
	StatusRecording = "recording"
	StatusFinished  = "finished"
	// StatusFailed means ffmpeg exited on its own before stop; whatever it
	// wrote is kept.
	StatusFailed = "failed"
)

// Errors mapped by the API layer.
var (
	ErrActive      = errors.New("a recording is already in progress")
	ErrNotFound    = errors.New("recording session not found")
	ErrStillActive = errors.New("the recording is still in progress; stop it first")
	ErrNoFFmpeg    = errors.New("ffmpeg with libx264 and a screen grabber is not available")
)

// stopGrace bounds the wait for ffmpeg to exit after "q" (spec 6.8).
const stopGrace = 5 * time.Second

// Session is the persisted metadata of one recording.
type Session struct {
	ID         string        `json:"session_id"`
	Status     string        `json:"status"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt *time.Time    `json:"finished_at"`
	DurationMS int64         `json:"duration_ms"`
	Size       int64         `json:"size"`
	SHA256     string        `json:"sha256,omitempty"`
	FPS        int           `json:"fps"`
	Rect       protocol.Rect `json:"rect"`
	Error      string        `json:"error,omitempty"`
}

// Options are the record/start parameters.
type Options struct {
	FPS    int
	Rect   protocol.Rect // API coordinates, already resolved and clipped
	Preset string
	CRF    int
}

// Presets are the x264 presets ffmpeg accepts.
var Presets = []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}

// Recorder manages the sessions in one directory.
type Recorder struct {
	dir       string
	ffmpeg    string // resolved executable, "" when unavailable
	quota     int64
	retention time.Duration
	now       func() time.Time
	// grabArgs builds the input arguments and stopGrace bounds the clean
	// exit after "q"; both are replaceable in tests.
	grabArgs  func(fps int, r protocol.Rect) ([]string, error)
	stopGrace time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
	active   *active
	closed   bool
}

type active struct {
	session *Session
	tree    *process.Tree
	stdin   io.WriteCloser
	stderr  *tailBuffer
	exited  chan struct{}
	exitErr error
	stopped bool
}

// Open loads dir. Sessions left recording by a crashed run are finalised
// from what is on disk (fragmented MP4 stays playable).
func Open(dir, ffmpeg string, quota int64, retention time.Duration, now func() time.Time) (*Recorder, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Recorder{dir: dir, ffmpeg: ffmpeg, quota: quota, retention: retention, now: now, grabArgs: grabArgs, stopGrace: stopGrace, sessions: map[string]*Session{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		s, err := readSession(filepath.Join(dir, e.Name()))
		if err != nil || s.ID != id {
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		if s.Status == StatusRecording {
			r.finalise(s, "vm-agent restarted during the recording")
			s.Status = StatusFailed
			_ = writeSession(r.metaPath(s.ID), s)
		}
		r.sessions[s.ID] = s
	}
	r.Prune()
	return r, nil
}

// SetGrabArgs replaces how ffmpeg's input arguments are built (tests).
func (r *Recorder) SetGrabArgs(f func(fps int, rect protocol.Rect) ([]string, error)) { r.grabArgs = f }

// Available reports whether recording is possible.
func (r *Recorder) Available() bool { return r.ffmpeg != "" }

func (r *Recorder) metaPath(id string) string  { return filepath.Join(r.dir, id+".json") }
func (r *Recorder) videoPath(id string) string { return filepath.Join(r.dir, id+".mp4") }

func readSession(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	return &s, json.Unmarshal(data, &s)
}

func writeSession(path string, s *Session) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "rec-" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// Start begins a recording. Only one may run at a time.
func (r *Recorder) Start(ctx context.Context, o Options) (*Session, error) {
	if r.ffmpeg == "" {
		return nil, ErrNoFFmpeg
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("vm-agent is shutting down")
	}
	if r.active != nil {
		r.mu.Unlock()
		return nil, ErrActive
	}
	// Reserve the slot while ffmpeg starts.
	placeholder := &active{}
	r.active = placeholder
	r.mu.Unlock()

	s, a, err := r.launch(ctx, o)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.active = nil
		return nil, err
	}
	r.active = a
	r.sessions[s.ID] = s
	go r.watch(a)
	c := *s
	return &c, nil
}

func (r *Recorder) launch(ctx context.Context, o Options) (*Session, *active, error) {
	input, err := r.grabArgs(o.FPS, o.Rect)
	if err != nil {
		return nil, nil, err
	}
	id := newID()
	args := append([]string{"-hide_banner", "-loglevel", "error"}, input...)
	args = append(args, "-c:v", "libx264", "-preset", o.Preset, "-crf", strconv.Itoa(o.CRF), "-pix_fmt", "yuv420p",
		"-movflags", "+frag_keyframe+empty_moov", "-y", r.videoPath(id))
	cmd := exec.Command(r.ffmpeg, args...)
	cmd.Env = ffmpegEnv(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr := &tailBuffer{max: 8 << 10}
	cmd.Stderr = stderr
	s := &Session{ID: id, Status: StatusRecording, StartedAt: r.now().UTC(), FPS: o.FPS, Rect: o.Rect}
	if err := writeSession(r.metaPath(id), s); err != nil {
		return nil, nil, err
	}
	tree, err := process.StartTree(cmd)
	if err != nil {
		os.Remove(r.metaPath(id))
		return nil, nil, err
	}
	a := &active{session: s, tree: tree, stdin: stdin, stderr: stderr, exited: make(chan struct{})}
	go func() { a.exitErr = tree.Wait(); close(a.exited) }()
	// ffmpeg reports bad devices and encoders within a moment; wait until it
	// has written the MP4 header or exited, whichever comes first.
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case <-a.exited:
			msg := strings.TrimSpace(stderr.String())
			os.Remove(r.metaPath(id))
			os.Remove(r.videoPath(id))
			return nil, nil, protocol.Errorf(protocol.CodeIOFailed, "ffmpeg exited at start: %s", firstLine(msg, a.exitErr))
		case <-ctx.Done():
			_ = tree.Kill()
			<-a.exited
			os.Remove(r.metaPath(id))
			os.Remove(r.videoPath(id))
			return nil, nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
		if info, err := os.Stat(r.videoPath(id)); (err == nil && info.Size() > 0) || time.Now().After(deadline) {
			return s, a, nil
		}
	}
}

func firstLine(msg string, err error) string {
	if msg == "" {
		return fmt.Sprint(err)
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[:i]
	}
	return msg
}

// watch notices ffmpeg exiting on its own (display gone, disk full).
func (r *Recorder) watch(a *active) {
	<-a.exited
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.stopped || r.active != a {
		return
	}
	r.finalise(a.session, "ffmpeg exited: "+firstLine(strings.TrimSpace(a.stderr.String()), a.exitErr))
	a.session.Status = StatusFailed
	_ = writeSession(r.metaPath(a.session.ID), a.session)
	r.active = nil
}

// finalise fills size, sha256 and duration from the file on disk.
func (r *Recorder) finalise(s *Session, errMsg string) {
	t := r.now().UTC()
	s.FinishedAt = &t
	s.DurationMS = t.Sub(s.StartedAt).Milliseconds()
	s.Error = errMsg
	f, err := os.Open(r.videoPath(s.ID))
	if err != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && errMsg != "" {
		// After a crash the file's mtime is the best end time we have.
		if mt := info.ModTime().UTC(); mt.After(s.StartedAt) && mt.Before(t) {
			s.FinishedAt = &mt
			s.DurationMS = mt.Sub(s.StartedAt).Milliseconds()
		}
	}
	h := sha256.New()
	n, _ := io.Copy(h, f)
	s.Size, s.SHA256 = n, hex.EncodeToString(h.Sum(nil))
}

// Stop ends the active recording: "q" on ffmpeg's stdin, up to 5 s for a
// clean exit, then the process tree is killed (the fragmented MP4 stays
// playable either way).
func (r *Recorder) Stop(id string) (*Session, error) {
	r.mu.Lock()
	a := r.active
	if a == nil || a.session == nil || a.session.ID != id {
		s, ok := r.sessions[id]
		r.mu.Unlock()
		if !ok {
			return nil, ErrNotFound
		}
		c := *s // already finished: stopping again reports the result
		return &c, nil
	}
	a.stopped = true
	r.mu.Unlock()
	r.stopActive(a)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalise(a.session, "")
	a.session.Status = StatusFinished
	err := writeSession(r.metaPath(id), a.session)
	r.active = nil
	r.pruneLocked()
	c := *a.session
	return &c, err
}

func (r *Recorder) stopActive(a *active) {
	_, _ = io.WriteString(a.stdin, "q\n")
	_ = a.stdin.Close()
	select {
	case <-a.exited:
	case <-time.After(r.stopGrace):
		_ = a.tree.Kill()
		<-a.exited
	}
}

// List returns every session, newest first.
func (r *Recorder) List() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Open returns a finished recording for download.
func (r *Recorder) OpenVideo(id string) (*os.File, *Session, error) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	recording := r.active != nil && r.active.session == s
	var c Session
	if ok {
		c = *s
	}
	r.mu.Unlock()
	if !ok {
		return nil, nil, ErrNotFound
	}
	if recording {
		return nil, nil, ErrStillActive
	}
	f, err := os.Open(r.videoPath(id))
	if err != nil {
		return nil, nil, err
	}
	return f, &c, nil
}

// Delete removes a finished recording.
func (r *Recorder) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if r.active != nil && r.active.session == s {
		return ErrStillActive
	}
	r.remove(id)
	return nil
}

func (r *Recorder) remove(id string) {
	os.Remove(r.videoPath(id))
	os.Remove(r.metaPath(id))
	delete(r.sessions, id)
}

// Prune deletes finished recordings past the retention and, while the
// directory exceeds the quota, the oldest finished recordings; either
// condition triggers (spec 6.8). It returns the removed session ids.
func (r *Recorder) Prune() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pruneLocked()
}

func (r *Recorder) pruneLocked() []string {
	var finished []*Session
	var total int64
	for _, s := range r.sessions {
		if r.active != nil && r.active.session == s {
			if info, err := os.Stat(r.videoPath(s.ID)); err == nil {
				total += info.Size()
			}
			continue
		}
		finished = append(finished, s)
		total += s.Size
	}
	sort.Slice(finished, func(i, j int) bool { return endOf(finished[i]).Before(endOf(finished[j])) })
	var removed []string
	cutoff := r.now().Add(-r.retention)
	for _, s := range finished {
		if endOf(s).Before(cutoff) || total > r.quota {
			total -= s.Size
			removed = append(removed, s.ID)
			r.remove(s.ID)
		}
	}
	return removed
}

func endOf(s *Session) time.Time {
	if s.FinishedAt != nil {
		return *s.FinishedAt
	}
	return s.StartedAt
}

// Active returns the session being recorded, if any.
func (r *Recorder) Active() *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil || r.active.session == nil {
		return nil
	}
	c := *r.active.session
	return &c
}

// Shutdown stops an active recording cleanly (spec 4.4 step 3) and refuses
// new ones.
func (r *Recorder) Shutdown() {
	r.mu.Lock()
	r.closed = true
	a := r.active
	id := ""
	if a != nil && a.session != nil {
		id = a.session.ID
	}
	r.mu.Unlock()
	if id != "" {
		_, _ = r.Stop(id)
	}
}

// tailBuffer keeps the last max bytes written (ffmpeg's error output).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// FindFFmpeg resolves the ffmpeg executable (configured path or PATH) and
// checks that it has libx264 and the platform's screen grabber, which the
// fixed command of spec section 10 needs. It returns "" when unusable.
func FindFFmpeg(configured string) (string, error) {
	path := configured
	if path == "" {
		var err error
		if path, err = exec.LookPath("ffmpeg"); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := func(arg string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, path, "-hide_banner", arg)
		process.HideConsole(cmd)
		return cmd.Output()
	}
	encoders, err := probe("-encoders")
	if err != nil {
		return "", fmt.Errorf("run %s: %w", path, err)
	}
	if !bytes.Contains(encoders, []byte("libx264")) {
		return "", fmt.Errorf("%s lacks the libx264 encoder", path)
	}
	devices, err := probe("-devices")
	if err != nil || !bytes.Contains(devices, []byte(grabberName)) {
		return "", fmt.Errorf("%s lacks the %s input device", path, grabberName)
	}
	return path, nil
}
