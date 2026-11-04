//go:build linux

package exec

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitStatus(t *testing.T, j *Jobs, id string, want string) *Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := j.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s stayed %s, want %s", id, job.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestJobRunsAndPersists(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJobs(dir, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, err := j.Start(Spec{Argv: []string{"sh", "-c", "echo out; echo err >&2; exit 5"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusRunning || job.PID == 0 {
		t.Fatalf("started = %+v", job)
	}
	done := waitStatus(t, j, job.ID, StatusExited)
	if *done.ExitCode != 5 || done.FinishedAt == nil || done.Reason != "" {
		t.Fatalf("done = %+v", done)
	}
	stdout, stderr, err := j.Output(job.ID, 0)
	if err != nil || string(stdout) != "out\n" || string(stderr) != "err\n" {
		t.Fatalf("output %q %q %v", stdout, stderr, err)
	}
	var onDisk Job
	data, _ := os.ReadFile(filepath.Join(dir, job.ID, "job.json"))
	json.Unmarshal(data, &onDisk)
	if onDisk.Status != StatusExited || *onDisk.ExitCode != 5 {
		t.Fatalf("disk = %+v", onDisk)
	}
}

func TestJobTailAndOutputLimit(t *testing.T) {
	j, _ := OpenJobs(t.TempDir(), time.Hour, nil)
	job, _ := j.Start(Spec{Argv: []string{"sh", "-c", "head -c 5000 /dev/zero | tr '\\0' a; printf END"}, OutputLimit: 3000}, 0)
	done := waitStatus(t, j, job.ID, StatusExited)
	if !done.Truncated {
		t.Fatal("not truncated")
	}
	all, _, _ := j.Output(job.ID, 0)
	tail, _, _ := j.Output(job.ID, 10)
	if len(all) != 3000 || len(tail) != 10 || strings.Contains(string(all), "END") {
		t.Fatalf("all=%d tail=%q", len(all), tail)
	}
}

func TestJobCancelAndTimeoutKillTree(t *testing.T) {
	j, _ := OpenJobs(t.TempDir(), time.Hour, nil)
	pidFile := filepath.Join(t.TempDir(), "pid")
	job, _ := j.Start(Spec{Argv: []string{"sh", "-c", "sleep 60 & echo $! > " + pidFile + "; wait"}}, 0)
	for i := 0; i < 200; i++ {
		if data, _ := os.ReadFile(pidFile); len(data) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := j.Cancel(job.ID)
	if err != nil || c.Status != StatusKilled || c.Reason != ReasonCancelled || c.ExitCode != nil {
		t.Fatalf("cancel = %+v %v", c, err)
	}
	assertGone(t, pidFile)
	if c, _ := j.Cancel(job.ID); c.Reason != ReasonCancelled {
		t.Fatal("second cancel changed the record")
	}
	timed, _ := j.Start(Spec{Argv: []string{"sleep", "60"}}, 100*time.Millisecond)
	if done := waitStatus(t, j, timed.ID, StatusKilled); done.Reason != ReasonJobTimeout {
		t.Fatalf("timeout = %+v", done)
	}
}

func TestJobDeleteAndRunningRefusal(t *testing.T) {
	dir := t.TempDir()
	j, _ := OpenJobs(dir, time.Hour, nil)
	job, _ := j.Start(Spec{Argv: []string{"sleep", "60"}}, 0)
	if err := j.Delete(job.ID); !errors.Is(err, ErrJobRunning) {
		t.Fatalf("delete running = %v", err)
	}
	j.Cancel(job.ID)
	if err := j.Delete(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, job.ID)); !os.IsNotExist(err) {
		t.Fatal("output directory left")
	}
	if _, err := j.Get(job.ID); !errors.Is(err, ErrJobNotFound) {
		t.Fatal("record left")
	}
}

func TestRestartMarksRunningJobsKilled(t *testing.T) {
	dir := t.TempDir()
	stale := &Job{ID: "job-stale", Argv0: "sleep", Status: StatusRunning, StartedAt: time.Now().UTC()}
	os.MkdirAll(filepath.Join(dir, stale.ID), 0o700)
	writeJob(filepath.Join(dir, stale.ID, "job.json"), stale)
	os.MkdirAll(filepath.Join(dir, "job-broken"), 0o700) // crashed before its first write
	j, err := OpenJobs(dir, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := j.Get("job-stale")
	if err != nil || got.Status != StatusKilled || got.Reason != ReasonAgentRestart || got.FinishedAt == nil {
		t.Fatalf("stale = %+v %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "job-stale", "job.json"))
	if !strings.Contains(string(data), ReasonAgentRestart) {
		t.Fatal("restart state not persisted")
	}
	if _, err := os.Stat(filepath.Join(dir, "job-broken")); !os.IsNotExist(err) {
		t.Fatal("broken job directory kept")
	}
}

func TestPruneRetentionAndCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	mk := func(id string, finished time.Time) {
		os.MkdirAll(filepath.Join(dir, id), 0o700)
		writeJob(filepath.Join(dir, id, "job.json"), &Job{ID: id, Status: StatusExited, StartedAt: finished, FinishedAt: &finished})
	}
	mk("job-old", now.Add(-25*time.Hour))
	for i := 0; i < MaxJobs+5; i++ {
		mk("job-"+leftPad(i), now.Add(-time.Duration(MaxJobs+5-i)*time.Second))
	}
	j, err := OpenJobs(dir, 24*time.Hour, clock)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(j.List()); n != MaxJobs {
		t.Fatalf("kept %d jobs, want %d", n, MaxJobs)
	}
	if _, err := j.Get("job-old"); err == nil {
		t.Fatal("job past retention kept")
	}
	if _, err := j.Get("job-" + leftPad(0)); err == nil {
		t.Fatal("oldest job beyond the count kept")
	}
	if _, err := j.Get("job-" + leftPad(MaxJobs+4)); err != nil {
		t.Fatal("newest job removed")
	}
}

func leftPad(i int) string {
	s := "0000" + itoa(i)
	return s[len(s)-5:]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestShutdownKillsRunningJobs(t *testing.T) {
	j, _ := OpenJobs(t.TempDir(), time.Hour, nil)
	job, _ := j.Start(Spec{Argv: []string{"sleep", "60"}}, 0)
	j.Shutdown()
	got, _ := j.Get(job.ID)
	if got.Status != StatusKilled || got.Reason != ReasonAgentShutdown {
		t.Fatalf("after shutdown = %+v", got)
	}
	if _, err := j.Start(Spec{Argv: []string{"true"}}, 0); err == nil {
		t.Fatal("start accepted after shutdown")
	}
}
