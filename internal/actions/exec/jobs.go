package exec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
)

// Job states (spec 6.5).
const (
	StatusRunning = "running"
	StatusExited  = "exited"
	StatusKilled  = "killed"
)

// Reasons a job was killed.
const (
	ReasonCancelled     = "cancelled"
	ReasonJobTimeout    = "job_timeout"
	ReasonAgentShutdown = "agent_shutdown"
	ReasonAgentRestart  = "agent_restart"
)

// MaxJobs bounds the finished jobs kept (spec 6.5).
const MaxJobs = 1000

// DefaultTail is the default tail_bytes of a job query.
const DefaultTail int64 = 64 << 10

// ErrJobNotFound means no job has the id.
var ErrJobNotFound = errors.New("job not found")

// ErrJobRunning means the job must finish before it can be deleted.
var ErrJobRunning = errors.New("job is still running")

// Job is the persisted metadata of a background command.
type Job struct {
	ID          string     `json:"job_id"`
	Argv0       string     `json:"argv0"`
	Argc        int        `json:"argc"`
	Status      string     `json:"status"`
	ExitCode    *int       `json:"exit_code"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Reason      string     `json:"reason,omitempty"`
	PID         int        `json:"pid"`
	OutputLimit int64      `json:"output_limit_bytes"`
	Truncated   bool       `json:"truncated"`
}

func (j *Job) finished() bool { return j.Status != StatusRunning }

// Jobs manages background commands under one directory: each job has
// dir/<id>/job.json plus stdout and stderr files. Metadata changes are
// written to disk before they are visible in memory, so a crash never loses
// a state transition the API has reported.
type Jobs struct {
	dir       string
	retention time.Duration
	now       func() time.Time

	mu      sync.Mutex
	jobs    map[string]*Job
	running map[string]*running
	closed  bool
	wg      sync.WaitGroup
}

type running struct {
	tree   *process.Tree
	cancel context.CancelFunc
	reason string // why it is being killed; set before killing
}

// OpenJobs loads dir, marking jobs left running by a previous process as
// killed with reason agent_restart, and applies retention.
func OpenJobs(dir string, retention time.Duration, now func() time.Time) (*Jobs, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	j := &Jobs{dir: dir, retention: retention, now: now, jobs: map[string]*Job{}, running: map[string]*running{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		job, err := readJob(filepath.Join(dir, e.Name(), "job.json"))
		if err != nil || job.ID != e.Name() {
			// A job directory without valid metadata is a crash before the
			// first write; nothing ran.
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			continue
		}
		if job.Status == StatusRunning {
			t := now().UTC()
			job.Status, job.Reason, job.FinishedAt = StatusKilled, ReasonAgentRestart, &t
			if err := writeJob(j.metaPath(job.ID), job); err != nil {
				return nil, err
			}
		}
		j.jobs[job.ID] = job
	}
	j.Prune()
	return j, nil
}

func (j *Jobs) metaPath(id string) string { return filepath.Join(j.dir, id, "job.json") }

func readJob(path string) (*Job, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// writeJob replaces the metadata file atomically.
func writeJob(path string, job *Job) error {
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "job-" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// Start runs spec in the background. timeout > 0 kills it after that long
// with reason job_timeout.
func (j *Jobs) Start(spec Spec, timeout time.Duration) (*Job, error) {
	cmd, err := Command(spec)
	if err != nil {
		return nil, err
	}
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil, errors.New("vm-agent is shutting down")
	}
	j.mu.Unlock()
	limit := OutputLimit(spec.OutputLimit)
	id := newJobID()
	jobDir := filepath.Join(j.dir, id)
	if err := os.Mkdir(jobDir, 0o700); err != nil {
		return nil, err
	}
	stdout, err := os.OpenFile(filepath.Join(jobDir, "stdout"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.RemoveAll(jobDir)
		return nil, err
	}
	stderr, err := os.OpenFile(filepath.Join(jobDir, "stderr"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		stdout.Close()
		os.RemoveAll(jobDir)
		return nil, err
	}
	outW, errW := &cappedFile{f: stdout, left: limit}, &cappedFile{f: stderr, left: limit}
	cmd.Stdout, cmd.Stderr = outW, errW
	if spec.Stdin != nil {
		cmd.Stdin = strings.NewReader(string(spec.Stdin))
	}
	job := &Job{ID: id, Argv0: spec.Argv[0], Argc: len(spec.Argv), Status: StatusRunning, StartedAt: j.now().UTC(), OutputLimit: limit}
	// The metadata exists before the process does, so a crash right after
	// the start is recovered as agent_restart rather than lost.
	if err := writeJob(j.metaPath(id), job); err != nil {
		stdout.Close()
		stderr.Close()
		os.RemoveAll(jobDir)
		return nil, err
	}
	tree, err := process.StartTree(cmd)
	if err != nil {
		stdout.Close()
		stderr.Close()
		os.RemoveAll(jobDir)
		return nil, err
	}
	job.PID = tree.Pid()
	_ = writeJob(j.metaPath(id), job)
	ctx, cancel := context.WithCancel(context.Background())
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	r := &running{tree: tree, cancel: cancel}
	j.mu.Lock()
	j.jobs[id] = job
	j.running[id] = r
	j.wg.Add(1)
	started := j.copyOf(job) // wait may update job as soon as it runs
	j.mu.Unlock()
	go j.wait(job, r, ctx, outW, errW, cmd)
	return started, nil
}

func (j *Jobs) wait(job *Job, r *running, ctx context.Context, outW, errW *cappedFile, cmd *osexec.Cmd) {
	defer j.wg.Done()
	stop := context.AfterFunc(ctx, func() {
		j.mu.Lock()
		if r.reason == "" {
			r.reason = ReasonJobTimeout
		}
		j.mu.Unlock()
		_ = r.tree.Kill()
	})
	_ = r.tree.Wait()
	stop()
	r.cancel()
	outW.f.Close()
	errW.f.Close()

	j.mu.Lock()
	defer j.mu.Unlock()
	updated := *job
	t := j.now().UTC()
	updated.FinishedAt = &t
	updated.Truncated = outW.truncated || errW.truncated
	if r.reason != "" {
		updated.Status, updated.Reason = StatusKilled, r.reason
	} else {
		code := cmd.ProcessState.ExitCode()
		updated.Status, updated.ExitCode = StatusExited, &code
	}
	// Disk first, then memory (spec 6.5).
	_ = writeJob(j.metaPath(job.ID), &updated)
	*job = updated
	delete(j.running, job.ID)
}

func (j *Jobs) copyOf(job *Job) *Job {
	c := *job
	return &c
}

// List returns every job, newest first.
func (j *Jobs) List() []*Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]*Job, 0, len(j.jobs))
	for _, job := range j.jobs {
		out = append(out, j.copyOf(job))
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].StartedAt.Equal(out[b].StartedAt) {
			return out[a].StartedAt.After(out[b].StartedAt)
		}
		return out[a].ID > out[b].ID
	})
	return out
}

// Get returns one job.
func (j *Jobs) Get(id string) (*Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	return j.copyOf(job), nil
}

// Output returns the last tail bytes (0: everything) of the job's stdout
// and stderr.
func (j *Jobs) Output(id string, tail int64) (stdout, stderr []byte, err error) {
	if _, err := j.Get(id); err != nil {
		return nil, nil, err
	}
	read := func(name string) ([]byte, error) {
		f, err := os.Open(filepath.Join(j.dir, id, name))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		start := int64(0)
		if tail > 0 && info.Size() > tail {
			start = info.Size() - tail
		}
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
		return io.ReadAll(f)
	}
	if stdout, err = read("stdout"); err != nil {
		return nil, nil, err
	}
	if stderr, err = read("stderr"); err != nil {
		return nil, nil, err
	}
	return stdout, stderr, nil
}

// Cancel kills a running job's process tree; the job then reports killed
// with reason cancelled. Cancelling a finished job is a no-op.
func (j *Jobs) Cancel(id string) (*Job, error) {
	j.mu.Lock()
	job, ok := j.jobs[id]
	if !ok {
		j.mu.Unlock()
		return nil, ErrJobNotFound
	}
	r := j.running[id]
	if r != nil && r.reason == "" {
		r.reason = ReasonCancelled
	}
	j.mu.Unlock()
	if r != nil {
		_ = r.tree.Kill()
		j.waitFinished(id, 10*time.Second)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.copyOf(job), nil
}

func (j *Jobs) waitFinished(id string, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		_, still := j.running[id]
		j.mu.Unlock()
		if !still {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Delete removes a finished job's record and output.
func (j *Jobs) Delete(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	job, ok := j.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if !job.finished() {
		return ErrJobRunning
	}
	if err := os.RemoveAll(filepath.Join(j.dir, id)); err != nil {
		return err
	}
	delete(j.jobs, id)
	return nil
}

// Prune deletes finished jobs older than the retention and, beyond MaxJobs
// finished jobs, the oldest ones (spec 6.5).
func (j *Jobs) Prune() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	var finished []*Job
	for _, job := range j.jobs {
		if job.finished() {
			finished = append(finished, job)
		}
	}
	sort.Slice(finished, func(a, b int) bool { return finishedAt(finished[a]).Before(finishedAt(finished[b])) })
	removed := 0
	cutoff := j.now().Add(-j.retention)
	for i, job := range finished {
		if finishedAt(job).Before(cutoff) || len(finished)-i > MaxJobs {
			if os.RemoveAll(filepath.Join(j.dir, job.ID)) == nil {
				delete(j.jobs, job.ID)
				removed++
			}
		}
	}
	return removed
}

func finishedAt(job *Job) time.Time {
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	return job.StartedAt
}

// Shutdown kills every running job with reason agent_shutdown and waits for
// their records to be written (spec 4.4 step 4).
func (j *Jobs) Shutdown() {
	j.mu.Lock()
	j.closed = true
	trees := make([]*process.Tree, 0, len(j.running))
	for _, r := range j.running {
		if r.reason == "" {
			r.reason = ReasonAgentShutdown
		}
		trees = append(trees, r.tree)
	}
	j.mu.Unlock()
	for _, t := range trees {
		_ = t.Kill()
	}
	done := make(chan struct{})
	go func() { j.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// Running reports the number of running jobs.
func (j *Jobs) Running() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.running)
}

// cappedFile writes up to left bytes and silently drops the rest so the
// command never sees a write error.
type cappedFile struct {
	f         *os.File
	left      int64
	truncated bool
}

func (c *cappedFile) Write(p []byte) (int, error) {
	n := len(p)
	if c.left <= 0 {
		if n > 0 {
			c.truncated = true
		}
		return n, nil
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
		c.truncated = true
	}
	w, err := c.f.Write(p)
	c.left -= int64(w)
	if err != nil {
		return n, fmt.Errorf("write job output: %w", err)
	}
	return n, nil
}
