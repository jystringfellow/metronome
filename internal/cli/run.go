package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

type exitStatus int

func (status exitStatus) Error() string { return fmt.Sprintf("command exited %d", status) }

func (a *app) run(ctx context.Context, args []string) error {
	separator := slices.Index(args, "--")
	if separator < 0 || separator == len(args)-1 {
		return usagef("give -- CMD [ARGS...]")
	}
	fs := newFlags("run")
	runner := fs.String("runner", "", "")
	slice := fs.String("slice", "", "")
	ws := fs.String("workstream", "", "")
	every := fs.Duration("every", 5*time.Minute, "")
	if err := parse(fs, args[:separator], 0); err != nil {
		return err
	}
	if *runner == "" {
		return usagef("--runner ID is required")
	}
	if *every <= 0 {
		return usagef("--every must be positive")
	}
	b, err := a.claim(ctx, *runner, *slice, *ws)
	if err != nil {
		if b.Claim.RunID != "" {
			a.closeRun(ctx, b.Claim.RunID, "", "brief could not be read")
		}
		return err
	}
	return a.runClaim(ctx, b, *runner, *every, args[separator+1:])
}

func (a *app) runClaim(ctx context.Context, b brief, runner string, every time.Duration, args []string) error {
	runID := b.Claim.RunID
	started := false
	defer func() {
		if !started {
			a.closeRun(ctx, runID, "", "command could not be started")
		}
	}()
	if err := b.writeText(a.errOut); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "metronome-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	briefPath, branchPath := filepath.Join(dir, "brief.json"), filepath.Join(dir, "branch")
	var data bytes.Buffer
	if err := writeJSON(&data, b); err != nil {
		return err
	}
	if err := os.WriteFile(briefPath, data.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(branchPath, nil, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"METRONOME_RUN_ID="+runID,
		"METRONOME_SLICE_ID="+b.Claim.SliceID,
		"METRONOME_RUNNER="+runner,
		"METRONOME_BRIEF="+briefPath,
		"METRONOME_BRANCH_FILE="+branchPath,
		"METRONOME_URL="+a.client.BaseURL,
		"METRONOME_ACTOR="+a.client.Actor,
	)
	var outputMu sync.Mutex
	a.out, a.errOut = guardedOutput(&outputMu, a.out), guardedOutput(&outputMu, a.errOut)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.out, a.errOut
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	terminal := a.terminal()
	if err := cmd.Start(); err != nil {
		return err
	}
	started = true
	renewCtx, stopRenewing := context.WithCancel(context.WithoutCancel(ctx))
	renewed := make(chan bool, 1)
	go func() { renewed <- a.renew(renewCtx, runID, every) }()
	code := a.waitCommand(ctx, cmd, signals, terminal)
	stopRenewing()
	closed := <-renewed
	if !closed {
		var branch, reason string
		if code == 0 {
			branch = a.readBranch(branchPath)
			reason = "no branch in $METRONOME_BRANCH_FILE"
		} else {
			reason = exitStatus(code).Error()
		}
		if err := a.closeRun(ctx, runID, branch, reason); err != nil && code == 0 {
			code = 1
		}
	}
	if code != 0 {
		return exitStatus(code)
	}
	return nil
}

type lockedOutput struct {
	mu *sync.Mutex
	w  io.Writer
}

func (out lockedOutput) Write(data []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	return out.w.Write(data)
}

func guardedOutput(mu *sync.Mutex, out io.Writer) io.Writer {
	if _, ok := out.(*os.File); ok {
		return out
	}
	return &lockedOutput{mu: mu, w: out}
}

func forwards(sig os.Signal, terminal bool) bool {
	return sig != os.Interrupt || !terminal
}

func (a *app) waitCommand(ctx context.Context, cmd *exec.Cmd, signals <-chan os.Signal, terminal bool) int {
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	cancelled := ctx.Done()
	for {
		select {
		case sig := <-signals:
			if forwards(sig, terminal) {
				_ = cmd.Process.Signal(sig)
			}
		case <-cancelled:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			cancelled = nil
		case err := <-waited:
			if err == nil {
				return 0
			}
			if cmd.ProcessState != nil {
				if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					return 128 + int(status.Signal())
				}
				if code := cmd.ProcessState.ExitCode(); code > 0 {
					return code
				}
			}
			fmt.Fprintf(a.errOut, "metronome run: %v\n", err)
			return 1
		}
	}
}

func (a *app) renew(ctx context.Context, runID string, every time.Duration) bool {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			err := a.client.Post(ctx, "/v1/runs/"+runID+"/heartbeat", "", api.ReportRequest{}, nil)
			if flow.CodeOf(err) == flow.CodeConflict {
				fmt.Fprintf(a.errOut, "metronome run: run %s is closed; stopped renewing: %v\n", runID, err)
				return true
			}
			if err != nil && ctx.Err() == nil {
				fmt.Fprintf(a.errOut, "metronome run: renew %s: %v\n", runID, err)
			}
		}
	}
}

func (a *app) readBranch(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(a.errOut, "metronome run: read branch: %v\n", err)
		}
		return ""
	}
	branch := strings.TrimSpace(string(data))
	if strings.ContainsAny(branch, "\r\n") {
		fmt.Fprintln(a.errOut, "metronome run: branch file contains multiple lines")
		return ""
	}
	return branch
}

func (a *app) closeRun(ctx context.Context, runID, branch, reason string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	action, message := "release", fmt.Sprintf("dropped %s (%s)", runID, reason)
	attempt := "drop"
	var body any
	if branch != "" {
		action, message = "submit", fmt.Sprintf("submitted %s on %s", runID, branch)
		attempt = "submit on " + branch
		body = api.SubmitRequest{Branch: branch}
	}
	err := a.client.Post(ctx, "/v1/runs/"+runID+"/"+action, "", body, nil)
	if flow.CodeOf(err) == flow.CodeConflict {
		fmt.Fprintf(a.errOut, "metronome run: run %s was already closed (wanted to %s): %v\n", runID, attempt, err)
		return nil
	}
	if err != nil {
		fmt.Fprintf(a.errOut, "metronome run: could not %s run %s: %v\n", action, runID, err)
		return err
	}
	fmt.Fprintln(a.errOut, "metronome run: "+message)
	return nil
}
