package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/exec"
	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/policy"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

// Default timeouts of spec section 7.
const (
	timeoutExec    = 30 * time.Second
	timeoutProcess = 10 * time.Second
)

func (a *API) processRoutes() []*server.Route {
	return []*server.Route{
		{Method: "POST", Path: "/v1/exec", Action: "exec", Class: server.NonRetryable, Handler: a.exec},
		{Method: "POST", Path: "/v1/process/launch", Action: "process.launch", Class: server.NonRetryable, Handler: a.processLaunch},
		{Method: "POST", Path: "/v1/process/open", Action: "process.open", Class: server.NonRetryable, Handler: a.processOpen},
		{Method: "GET", Path: "/v1/process/list", Action: "process.list", Class: server.Retryable, Handler: a.processList},
		{Method: "POST", Path: "/v1/process/kill", Action: "process.kill", Class: server.NonRetryable, Handler: a.processKill},
	}
}

func (a *API) processList(c *server.Call) (any, error) {
	name := c.Request.URL.Query().Get("name")
	procs, err := async.Run(c.Context(timeoutProcess), func() ([]process.Info, error) { return process.List(name) })
	if err != nil {
		return nil, err
	}
	return map[string]any{"processes": procs}, nil
}

// defaultGrace is the process/kill grace period (spec 6.5).
const defaultGrace = 3 * time.Second

func (a *API) processKill(c *server.Call) (any, error) {
	var req protocol.KillRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if req.PID <= 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "pid must be positive")
	}
	grace := defaultGrace
	if req.GraceMS != nil {
		if *req.GraceMS < 0 || *req.GraceMS > 600000 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "grace_ms must be between 0 and 600000")
		}
		grace = time.Duration(*req.GraceMS) * time.Millisecond
	}
	c.Param("pid", strconv.Itoa(req.PID))
	c.Param("tree", strconv.FormatBool(req.Tree))
	c.Param("force", strconv.FormatBool(req.Force))
	// Spec section 7: 10 s plus grace_ms.
	ctx := c.Context(timeoutProcess + grace)
	protected := map[int]bool{os.Getpid(): true, os.Getppid(): a.supervised}
	res, err := process.Kill(ctx, process.KillRequest{PID: req.PID, Tree: req.Tree, Grace: grace, Force: req.Force}, protected)
	switch {
	case errors.Is(err, process.ErrNotFound):
		return nil, protocol.Errorf(protocol.CodeNotFound, "process %d does not exist", req.PID)
	case errors.Is(err, process.ErrProtected):
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "process %d is vm-agent or its supervisor", req.PID)
	case errors.Is(err, process.ErrUnsupported):
		return nil, protocol.Unsupported("%v", err)
	}
	return res, err
}

// auditArgv records the spec section 8 summary of a command: the first two
// argv items, the argument count, the sha256 of the JSON-encoded argv and
// the environment keys; -audit-verbose adds the full argv.
func auditArgv(c *server.Call, argv []string, env map[string]string) {
	if len(argv) > 0 {
		c.Param("argv0", argv[0])
	}
	if len(argv) > 1 {
		c.Param("argv1", argv[1])
	}
	c.Param("argc", strconv.Itoa(len(argv)))
	data, _ := json.Marshal(argv)
	sum := sha256.Sum256(data)
	c.Param("argv_sha256", hex.EncodeToString(sum[:]))
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.Param("env_keys", strings.Join(keys, ","))
	}
	if c.Verbose() {
		c.Param("argv", string(data))
	}
}

func validateArgv(argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "argv must contain at least the program")
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "argv items must not contain NUL")
		}
	}
	return nil
}

func (a *API) checkPolicy(argv0 string) error {
	if err := a.policy.Check(argv0); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "%v", err).WithReason(protocol.ReasonPolicyDenied)
	}
	return nil
}

// commandError maps failures to start a command.
func commandError(err error) error {
	switch {
	case errors.Is(err, osexec.ErrNotFound):
		return protocol.Errorf(protocol.CodeNotFound, "%v", err)
	case errors.Is(err, exec.ErrInvalidEnv):
		return protocol.Errorf(protocol.CodeInvalidRequest, "%v", err)
	case errors.Is(err, policy.ErrDenied):
		return protocol.Errorf(protocol.CodeInvalidRequest, "%v", err).WithReason(protocol.ReasonPolicyDenied)
	}
	return err
}

func (a *API) exec(c *server.Call) (any, error) {
	var req protocol.ExecRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if err := validateArgv(req.Argv); err != nil {
		return nil, err
	}
	auditArgv(c, req.Argv, req.Env)
	if req.Detach {
		c.Param("detach", "true")
	}
	if req.OutputLimitBytes < 0 || req.JobTimeoutMS < 0 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "output_limit_bytes and job_timeout_ms must not be negative")
	}
	var stdin []byte
	if req.Stdin != nil {
		var err error
		if stdin, err = base64.StdEncoding.DecodeString(*req.Stdin); err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "stdin must be base64: %v", err)
		}
	}
	if err := a.checkPolicy(req.Argv[0]); err != nil {
		return nil, err
	}
	spec := exec.Spec{Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, EnvClear: req.EnvClear, Stdin: stdin, OutputLimit: req.OutputLimitBytes}
	if req.Detach {
		return a.execDetached(c, spec, req.JobTimeoutMS)
	}
	ctx := c.Context(timeoutExec)
	res, err := exec.Run(ctx, spec)
	if err != nil {
		if isContextError(err) {
			return nil, err
		}
		return nil, commandError(err)
	}
	c.Param("exit_code", strconv.Itoa(res.ExitCode))
	return protocol.ExecResult{
		ExitCode:   res.ExitCode,
		Stdout:     exec.DecodeOutput(req.Argv[0], res.Stdout),
		Stderr:     exec.DecodeOutput(req.Argv[0], res.Stderr),
		Truncated:  res.Truncated,
		DurationMS: res.Duration.Milliseconds(),
	}, nil
}

func (a *API) processLaunch(c *server.Call) (any, error) {
	var req protocol.LaunchRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if err := validateArgv(req.Argv); err != nil {
		return nil, err
	}
	auditArgv(c, req.Argv, req.Env)
	if err := a.checkPolicy(req.Argv[0]); err != nil {
		return nil, err
	}
	cmd, err := exec.Command(exec.Spec{Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, EnvClear: req.EnvClear})
	if err != nil {
		return nil, commandError(err)
	}
	if cmd.Err != nil {
		return nil, commandError(cmd.Err)
	}
	pid, err := async.Run(c.Context(timeoutProcess), func() (int, error) {
		return process.Launch(process.Command{Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir, Env: cmd.Env})
	})
	if err != nil {
		return nil, commandError(err)
	}
	c.Param("pid", strconv.Itoa(pid))
	return protocol.LaunchResult{PID: pid}, nil
}

func (a *API) processOpen(c *server.Call) (any, error) {
	var req protocol.OpenRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if (req.Path == "") == (req.URL == "") {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "give exactly one of path or url")
	}
	target, isURL := req.Path, req.URL != ""
	if isURL {
		u, err := url.Parse(req.URL)
		if err != nil || u.Scheme == "" {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "url is not an absolute URL")
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "file":
		default:
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "url scheme %q is not allowed; use http, https or file", u.Scheme).WithReason(protocol.ReasonURLScheme)
		}
		target = req.URL
		c.Param("url", req.URL)
	} else {
		if !filepath.IsAbs(req.Path) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "path must be absolute").WithReason(protocol.ReasonPathNotAbsolute)
		}
		c.Param("path", req.Path)
	}
	env, err := exec.Environ(nil, false)
	if err != nil {
		return nil, err
	}
	res, err := async.Run(c.Context(timeoutProcess), func() (process.OpenResult, error) {
		return process.Open(target, isURL, env)
	})
	if err != nil {
		if isContextError(err) {
			return nil, err
		}
		if errors.Is(err, process.ErrNoHandler) {
			return nil, protocol.Errorf(protocol.CodeNotFound, "%v", err)
		}
		if errors.Is(err, process.ErrUnsupported) {
			return nil, protocol.Unsupported("%v", err)
		}
		return nil, err
	}
	return res, nil
}

// execDetached is the background mode, which phase 2 implements.
func (a *API) execDetached(c *server.Call, spec exec.Spec, jobTimeoutMS int64) (any, error) {
	return nil, protocol.Unsupported("background exec (detach=true) is not available yet")
}
