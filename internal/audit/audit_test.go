package audit

import "testing"

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
