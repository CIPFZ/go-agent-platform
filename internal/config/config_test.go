package config

import (
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDefaultsMatchSpec(t *testing.T) {
	cfg, err := Parse(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	base := DefaultBaseDir()
	want := &Config{
		Listen:          "0.0.0.0:7070",
		AuditLog:        filepath.Join(base, "audit", "audit.jsonl"),
		AuditMaxSize:    100 << 20,
		AuditMaxFiles:   10,
		DedupWindow:     60 * time.Second,
		MaxConcurrency:  32,
		RecordDir:       filepath.Join(base, "records"),
		RecordQuota:     2 << 30,
		RecordRetention: 24 * time.Hour,
		JobDir:          filepath.Join(base, "jobs"),
		JobRetention:    24 * time.Hour,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("defaults:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestParseValues(t *testing.T) {
	root := t.TempDir()
	cfg, err := Parse([]string{"-listen", "127.0.0.1:1", "-audit-max-size", "5MiB", "-record-quota", "1GiB", "-root", root + " , " + root + "/sub", "-dedup-window", "0", "-supervise"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditMaxSize != 5<<20 || cfg.RecordQuota != 1<<30 || cfg.DedupWindow != 0 || !cfg.Supervise || len(cfg.Roots) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, bad := range [][]string{{"-root", "relative"}, {"-max-concurrency", "0"}, {"-audit-max-size", "x"}, {"extra"}} {
		if _, err := Parse(bad, io.Discard); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"10": 10, "1KiB": 1024, "100MiB": 100 << 20, "2GiB": 2 << 30, "7B": 7} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
}

func TestChildArgsDropsSupervise(t *testing.T) {
	got := ChildArgs([]string{"-supervise", "-listen", ":1", "--supervise=true", "-job-dir", "x"})
	want := []string{"-listen", ":1", "-job-dir", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}
