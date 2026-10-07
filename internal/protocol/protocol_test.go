package protocol

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEnvelopeValidate(t *testing.T) {
	valid := Envelope{
		ProtocolVersion: Version,
		RequestID:       "request-1",
		Timestamp:       time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}

	tests := []struct {
		name string
		in   Envelope
		want error
	}{
		{name: "version", in: Envelope{ProtocolVersion: "2", RequestID: "r", Timestamp: valid.Timestamp}, want: ErrUnsupportedVersion},
		{name: "request id", in: Envelope{ProtocolVersion: Version, Timestamp: valid.Timestamp}, want: ErrMissingRequestID},
		{name: "timestamp", in: Envelope{ProtocolVersion: Version, RequestID: "r"}, want: ErrMissingTimestamp},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.in.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAgentJSONOmitsOptionalFields(t *testing.T) {
	data, err := json.Marshal(&Agent{
		AgentID:      "agent-1",
		AgentVersion: "1.0.0",
		Status:       AgentStatusOnline,
	})
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}
	if string(data) == "" || string(data) == "null" {
		t.Fatalf("unexpected agent JSON: %s", data)
	}
}
