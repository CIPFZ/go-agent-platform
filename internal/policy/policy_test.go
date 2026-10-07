package policy

import (
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestEngineDecisions(t *testing.T) {
	action := protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"echo", "hello"}}}
	engine := Engine{AllowedCommands: map[string]bool{"echo": true}, RequireConfirm: map[string]bool{"echo": true}}
	result := engine.Evaluate(action)
	if result.Decision != DecisionConfirm {
		t.Fatalf("decision=%+v", result)
	}
	engine.RequireConfirm = nil
	if result = engine.Evaluate(action); result.Decision != DecisionAllow {
		t.Fatalf("allow decision=%+v", result)
	}
	engine.AllowedCommands = map[string]bool{"printf": true}
	if result = engine.Evaluate(action); result.Decision != DecisionDeny {
		t.Fatalf("deny decision=%+v", result)
	}
}
