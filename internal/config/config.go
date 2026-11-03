// Package config parses the vm-agent command line (spec section 4.3).
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the parsed command line.
type Config struct {
	Listen          string
	AuditLog        string
	AuditMaxSize    int64
	AuditMaxFiles   int
	AuditVerbose    bool
	Supervise       bool
	ExecAllowlist   string
	ExecDenylist    string
	DedupWindow     time.Duration
	MaxConcurrency  int
	Roots           []string
	FFmpeg          string
	RecordDir       string
	RecordQuota     int64
	RecordRetention time.Duration
	JobDir          string
	JobRetention    time.Duration
}

// DefaultBaseDir is the per-user state root used by the defaults: the user
// temporary directory joined with "vm-agent".
func DefaultBaseDir() string { return filepath.Join(os.TempDir(), "vm-agent") }

// Parse parses args (without the program name).
func Parse(args []string, output io.Writer) (*Config, error) {
	base := DefaultBaseDir()
	cfg := &Config{}
	fs := flag.NewFlagSet("vm-agent", flag.ContinueOnError)
	fs.SetOutput(output)
	auditMax := byteSize(100 << 20)
	recordQuota := byteSize(2 << 30)
	var roots string
	fs.StringVar(&cfg.Listen, "listen", "0.0.0.0:7070", "listen address")
	fs.StringVar(&cfg.AuditLog, "audit-log", filepath.Join(base, "audit", "audit.jsonl"), "audit JSONL path")
	fs.Var(&auditMax, "audit-max-size", "rotate the audit file at this size (e.g. 100MiB)")
	fs.IntVar(&cfg.AuditMaxFiles, "audit-max-files", 10, "number of audit files to keep")
	fs.BoolVar(&cfg.AuditVerbose, "audit-verbose", false, "audit full argv and typed text (troubleshooting only)")
	fs.BoolVar(&cfg.Supervise, "supervise", false, "supervisor mode: restart the agent child process (Windows)")
	fs.StringVar(&cfg.ExecAllowlist, "exec-allowlist-file", "", "optional command allowlist file, one name or absolute path per line")
	fs.StringVar(&cfg.ExecDenylist, "exec-denylist-file", "", "optional command denylist file, same format; checked before the allowlist")
	fs.DurationVar(&cfg.DedupWindow, "dedup-window", 60*time.Second, "request_id deduplication window, 0 disables")
	fs.IntVar(&cfg.MaxConcurrency, "max-concurrency", 32, "global in-flight request limit")
	fs.StringVar(&roots, "root", "", "optional comma-separated directories file operations are limited to")
	fs.StringVar(&cfg.FFmpeg, "ffmpeg", "", "ffmpeg executable; empty searches PATH")
	fs.StringVar(&cfg.RecordDir, "record-dir", filepath.Join(base, "records"), "recording directory")
	fs.Var(&recordQuota, "record-quota", "total size limit of the recording directory (e.g. 2GiB)")
	fs.DurationVar(&cfg.RecordRetention, "record-retention", 24*time.Hour, "retention of finished recordings")
	fs.StringVar(&cfg.JobDir, "job-dir", filepath.Join(base, "jobs"), "background command output directory")
	fs.DurationVar(&cfg.JobRetention, "job-retention", 24*time.Hour, "retention of finished background jobs")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %q", fs.Args())
	}
	cfg.AuditMaxSize = int64(auditMax)
	cfg.RecordQuota = int64(recordQuota)
	for _, root := range strings.Split(roots, ",") {
		if root = strings.TrimSpace(root); root != "" {
			if !filepath.IsAbs(root) {
				return nil, fmt.Errorf("-root %q must be an absolute path", root)
			}
			cfg.Roots = append(cfg.Roots, filepath.Clean(root))
		}
	}
	switch {
	case cfg.AuditMaxSize <= 0:
		return nil, errors.New("-audit-max-size must be positive")
	case cfg.AuditMaxFiles < 1:
		return nil, errors.New("-audit-max-files must be at least 1")
	case cfg.DedupWindow < 0:
		return nil, errors.New("-dedup-window must not be negative")
	case cfg.MaxConcurrency < 1:
		return nil, errors.New("-max-concurrency must be at least 1")
	case cfg.RecordQuota <= 0:
		return nil, errors.New("-record-quota must be positive")
	case cfg.RecordRetention <= 0 || cfg.JobRetention <= 0:
		return nil, errors.New("retention durations must be positive")
	}
	return cfg, nil
}

// ChildArgs returns args with every -supervise flag removed, for the child
// started by supervisor mode (spec section 4.1).
func ChildArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		name := strings.TrimLeft(arg, "-")
		if strings.HasPrefix(arg, "-") && (name == "supervise" || strings.HasPrefix(name, "supervise=")) {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// byteSize is a flag value accepting plain bytes or a KiB/MiB/GiB suffix.
type byteSize int64

func (b *byteSize) String() string { return strconv.FormatInt(int64(*b), 10) }

func (b *byteSize) Set(value string) error {
	n, err := ParseSize(value)
	if err != nil {
		return err
	}
	*b = byteSize(n)
	return nil
}

// ParseSize parses "1048576", "512KiB", "100MiB" or "2GiB".
func ParseSize(value string) (int64, error) {
	value = strings.TrimSpace(value)
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		factor int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"B", 1}} {
		if strings.HasSuffix(value, unit.suffix) {
			value, multiplier = strings.TrimSpace(strings.TrimSuffix(value, unit.suffix)), unit.factor
			break
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/multiplier {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	return n * multiplier, nil
}
