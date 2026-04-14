package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type AuditRecorder interface {
	RecordSystemAction(actorUserID *uint, action string, payload string, ip string)
}

type CommandRequest struct {
	Binary      string
	Args        []string
	WorkingDir  string
	Timeout     time.Duration
	Stdin       string
	AuditAction string
	ActorUserID *uint
	IP          string
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

type Runner struct {
	audit AuditRecorder
}

func NewRunner(audit AuditRecorder) *Runner {
	return &Runner{audit: audit}
}

func (r *Runner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if strings.TrimSpace(req.Binary) == "" {
		return CommandResult{}, errors.New("binary is required")
	}
	if strings.Contains(req.Binary, " ") {
		return CommandResult{}, errors.New("binary path cannot contain whitespace")
	}
	for _, arg := range req.Args {
		if strings.ContainsRune(arg, '\x00') {
			return CommandResult{}, errors.New("invalid argument: contains NUL")
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Binary, req.Args...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)
	result := CommandResult{Stdout: outBuf.String(), Stderr: errBuf.String(), Duration: duration}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	if r.audit != nil && req.AuditAction != "" {
		r.audit.RecordSystemAction(req.ActorUserID, req.AuditAction, fmt.Sprintf("%s %s", req.Binary, strings.Join(req.Args, " ")), req.IP)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("command failed: %w; stderr=%s", err, strings.TrimSpace(result.Stderr))
	}
	return result, nil
}
