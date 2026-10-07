package policy

import (
	"path/filepath"
	"strings"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type Decision string

const (
	DecisionAllow   Decision = "allow"
	DecisionDeny    Decision = "deny"
	DecisionConfirm Decision = "confirm"
)

type Result struct {
	Decision Decision
	Rule     string
	Reason   string
}

type Engine struct {
	AllowedCommands map[string]bool
	RequireConfirm  map[string]bool
}

func (e Engine) Evaluate(action protocol.TaskAction) Result {
	if err := action.Validate(); err != nil {
		return Result{Decision: DecisionDeny, Rule: "schema", Reason: err.Error()}
	}
	if action.Type == protocol.ActionCommandExec {
		program := filepath.Base(action.Command.Argv[0])
		if len(e.AllowedCommands) > 0 && !e.AllowedCommands[program] && !e.AllowedCommands[action.Command.Argv[0]] {
			return Result{Decision: DecisionDeny, Rule: "allowlist", Reason: "command is not allowlisted"}
		}
		if e.RequireConfirm[program] || e.RequireConfirm[action.Command.Argv[0]] {
			return Result{Decision: DecisionConfirm, Rule: "confirmation", Reason: "command requires explicit confirmation"}
		}
		for _, arg := range action.Command.Argv {
			if strings.ContainsAny(arg, "\x00\n\r") {
				return Result{Decision: DecisionDeny, Rule: "argument", Reason: "command argument contains a control character"}
			}
		}
	}
	return Result{Decision: DecisionAllow, Rule: "default", Reason: "action allowed"}
}
