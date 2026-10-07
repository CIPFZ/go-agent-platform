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
