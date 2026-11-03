package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Event is one audit record. vm-agent writes one "request" event per HTTP
// request (spec section 8) plus lifecycle events such as "startup" and
// "shutdown".
type Event struct {
	Time       time.Time         `json:"time"`
	Kind       string            `json:"kind"`
	Actor      string            `json:"actor,omitempty"`
	ResourceID string            `json:"resource_id,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	Details    map[string]string `json:"details,omitempty"`

	// Request fields.
	RequestID string            `json:"request_id,omitempty"`
	Remote    string            `json:"remote,omitempty"`
	Action    string            `json:"action,omitempty"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Code      string            `json:"code,omitempty"`
	ElapsedMS int64             `json:"elapsed_ms"`
	DedupHit  bool              `json:"dedup_hit"`
	Params    map[string]string `json:"params,omitempty"`
}

type Sink interface{ Record(Event) }

type MemorySink struct {
	mu     sync.RWMutex
	events []Event
	limit  int
}

// FileSink appends one JSON event per line and is safe for concurrent writers.
// It keeps the last write error so health checks and operators can detect a
// degraded audit trail instead of silently losing security events.
//
// With a size limit the file is rotated before a write would push it past
// MaxSize: path becomes path.1, path.1 becomes path.2 and so on, keeping
// MaxFiles files in total including the active one.
type FileSink struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	maxFiles int
	size     int64 // bytes in the active file; -1 until first stat
	lastErr  error
}

func NewFileSink(path string) (*FileSink, error) {
	return NewRotatingFileSink(path, 0, 0)
}

// NewRotatingFileSink returns a FileSink that rotates at maxSize bytes and
// keeps maxFiles files. maxSize <= 0 disables rotation.
func NewRotatingFileSink(path string, maxSize int64, maxFiles int) (*FileSink, error) {
	if path == "" {
		return nil, errors.New("audit path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if maxFiles < 1 {
		maxFiles = 1
	}
	return &FileSink{path: path, maxSize: maxSize, maxFiles: maxFiles, size: -1}, nil
}

func (s *FileSink) Record(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Details = clone(event.Details)
	event.Params = clone(event.Params)
	data, err := json.Marshal(event)
	if err == nil {
		data = append(data, '\n')
		err = s.rotateFor(int64(len(data)))
	}
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			var n int
			n, err = file.Write(data)
			s.size += int64(n)
			if syncErr := file.Sync(); err == nil {
				err = syncErr
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
	}
	s.lastErr = err
}

func (s *FileSink) rotateFor(next int64) error {
	if s.size < 0 {
		s.size = 0
		if info, err := os.Stat(s.path); err == nil {
			s.size = info.Size()
		}
	}
	if s.maxSize <= 0 || s.size == 0 || s.size+next <= s.maxSize {
		return nil
	}
	if s.maxFiles == 1 {
		s.size = 0
		return os.Truncate(s.path, 0)
	}
	_ = os.Remove(s.rotated(s.maxFiles - 1))
	for i := s.maxFiles - 2; i >= 1; i-- {
		if err := os.Rename(s.rotated(i), s.rotated(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(s.path, s.rotated(1)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.size = 0
	return nil
}

func (s *FileSink) rotated(i int) string { return s.path + "." + strconv.Itoa(i) }

func (s *FileSink) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func NewMemorySink(limit int) *MemorySink {
	if limit <= 0 {
		limit = 10000
	}
	return &MemorySink{limit: limit}
}

func (s *MemorySink) Record(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Details = clone(event.Details)
	event.Params = clone(event.Params)
	s.events = append(s.events, event)
	if len(s.events) > s.limit {
		s.events = append([]Event(nil), s.events[len(s.events)-s.limit:]...)
	}
}

func (s *MemorySink) List() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	output := make([]Event, len(s.events))
	copy(output, s.events)
	for i := range output {
		output[i].Details = clone(output[i].Details)
	}
	return output
}

func clone(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
