package audit

import (
	"sync"
	"time"
)

type Event struct {
	Time       time.Time         `json:"time"`
	Kind       string            `json:"kind"`
	Actor      string            `json:"actor,omitempty"`
	ResourceID string            `json:"resource_id,omitempty"`
	Outcome    string            `json:"outcome,omitempty"`
	Details    map[string]string `json:"details,omitempty"`
}

type Sink interface{ Record(Event) }

type MemorySink struct {
	mu     sync.RWMutex
	events []Event
	limit  int
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
