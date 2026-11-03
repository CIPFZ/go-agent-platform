package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestFileWriteReadRoundTrip(t *testing.T) {
	e := newEnv(t)
	p := filepath.Join(t.TempDir(), "docs", "note.txt")
	content := base64.StdEncoding.EncodeToString([]byte("hello file"))
	var w struct {
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	e.call("POST", "/v1/file/write", map[string]any{"path": p, "content": content, "mkdir": true}).into(t, &w)
	if w.Size != 10 || len(w.SHA256) != 64 {
		t.Fatalf("write = %+v", w)
	}
	e.call("POST", "/v1/file/write", map[string]any{"path": p, "content": content}).code(t, protocol.CodeConflict)
	var r protocol.FileReadResult
	e.call("POST", "/v1/file/read", map[string]any{"path": p}).into(t, &r)
	data, _ := base64.StdEncoding.DecodeString(r.Content)
	if string(data) != "hello file" || r.Size != 10 || r.SHA256 != w.SHA256 || r.Mtime.IsZero() {
		t.Fatalf("read = %+v", r)
	}
	s := e.call("POST", "/v1/file/read", map[string]any{"path": p, "offset": 6, "encoding": "stream"})
	if s.Status != 200 || string(s.Body) != "file" || s.Header.Get(protocol.HeaderResultSize) != "4" || s.Header.Get(protocol.HeaderFileMtime) == "" || len(s.Header.Get(protocol.HeaderResultSHA256)) != 64 {
		t.Fatalf("stream = %d %q %v", s.Status, s.Body, s.Header)
	}
	e.lastAudit("file.read")
	for _, x := range e.auditEvents(4) {
		if x.Action == "file.write" && (x.Params["path"] != p || x.Params["content"] != "") {
			t.Fatalf("write audit = %v", x.Params)
		}
	}
}

func TestFileStatListMkdirKnownDirs(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("1"), 0o600)
	var st map[string]any
	e.call("POST", "/v1/file/stat", map[string]any{"path": filepath.Join(dir, "a")}).into(t, &st)
	if st["exists"] != true || st["type"] != "file" || st["size"] != float64(1) {
		t.Fatalf("stat = %v", st)
	}
	e.call("POST", "/v1/file/stat", map[string]any{"path": filepath.Join(dir, "missing")}).into(t, &st)
	if st["exists"] != false {
		t.Fatalf("missing stat = %v", st)
	}
	e.call("POST", "/v1/file/mkdir", map[string]any{"path": filepath.Join(dir, "x", "y")}).into(t, &st)
	var list struct {
		Entries   []map[string]any `json:"entries"`
		Truncated bool             `json:"truncated"`
	}
	e.call("POST", "/v1/file/list", map[string]any{"path": dir, "max_entries": 1}).into(t, &list)
	if len(list.Entries) != 1 || !list.Truncated {
		t.Fatalf("list = %+v", list)
	}
	e.call("POST", "/v1/file/list", map[string]any{"path": dir, "max_entries": 0}).code(t, protocol.CodeInvalidRequest)
	var known map[string]string
	e.call("GET", "/v1/file/known_dirs", nil).into(t, &known)
	if known["home"] == "" || known["temp"] == "" {
		t.Fatalf("known = %v", known)
	}
	e.call("POST", "/v1/file/read", map[string]any{"path": "relative"}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/file/read", map[string]any{"path": filepath.Join(dir, "nope")}).code(t, protocol.CodeNotFound)
}

func TestFileRootRestriction(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, func(d *Deps) { d.Config.Roots = []string{root} })
	r := e.call("POST", "/v1/file/stat", map[string]any{"path": filepath.Join(t.TempDir(), "x")})
	r.code(t, protocol.CodeInvalidRequest)
	if r.Error.Details["reason"] != protocol.ReasonOutsideRoot {
		t.Fatalf("reason = %v", r.Error.Details)
	}
	var st map[string]any
	e.call("POST", "/v1/file/stat", map[string]any{"path": filepath.Join(root, "x")}).into(t, &st)
}
