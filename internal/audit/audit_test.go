package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMemorySinkCopiesAndBoundsEvents(t *testing.T) {
	sink := NewMemorySink(2)
	details := map[string]string{"secret": "redacted"}
	sink.Record(Event{Kind: "one", Details: details})
	details["secret"] = "changed"
	sink.Record(Event{Kind: "two"})
	sink.Record(Event{Kind: "three"})
	events := sink.List()
	if len(events) != 2 || events[0].Kind != "two" || events[1].Kind != "three" {
		t.Fatalf("unexpected bounded events: %+v", events)
	}
}

func TestFileSinkWritesJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "events.jsonl")
	sink, err := NewFileSink(path)
	if err != nil {
		t.Fatalf("new file sink: %v", err)
	}
	sink.Record(Event{Kind: "task.create", ResourceID: "task-1", Details: map[string]string{"action": "command.exec"}})
	if err := sink.LastError(); err != nil {
		t.Fatalf("write audit event: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("audit file is empty")
	}
	var event Event
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
		t.Fatalf("decode audit event: %v", err)
	}
	if event.Kind != "task.create" || event.Details["action"] != "command.exec" {
		t.Fatalf("audit event = %+v", event)
	}
}

func TestFileSinkRotatesBySizeAndKeepsMaxFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewRotatingFileSink(path, 400, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		sink.Record(Event{Kind: "request", RequestID: "request-id-0000000000", Action: "input.mouse.click", Code: "ok"})
		if err := sink.LastError(); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
		if info.Size() > 400 {
			t.Fatalf("%s is %d bytes, over the 400 byte limit", name, info.Size())
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("more than 3 files kept: %v", err)
	}
}

func TestFileSinkResumesSizeOfExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, make([]byte, 390), 0o600); err != nil {
		t.Fatal(err)
	}
	sink, _ := NewRotatingFileSink(path, 400, 2)
	sink.Record(Event{Kind: "startup"})
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("pre-existing content not counted toward rotation: %v", err)
	}
}
