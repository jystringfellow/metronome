package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"github.com/jystringfellow/metronome/internal/githubapp"
)

type exitStatus int

func (status exitStatus) Error() string { return fmt.Sprintf("command exited %d", status) }

const (
	closeInitialDelay   = time.Second
	closeMaxDelay       = 30 * time.Second
	closeLeaseGrace     = time.Minute
	closeBound          = 30 * time.Minute
	closeAttemptTimeout = 10 * time.Second
)

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
	cmd.Env = append(githubapp.WithoutAppSettings(os.Environ()),
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
	renewed := make(chan renewal, 1)
	go func() { renewed <- a.renew(renewCtx, runID, every, b.Claim.LeaseUntil) }()
	code := a.waitCommand(ctx, cmd, signals, terminal)
	stopRenewing()
	lease := <-renewed
	if !lease.closed {
		var branch, reason string
		if code == 0 {
			branch = a.readBranch(branchPath)
			reason = "no branch in $METRONOME_BRANCH_FILE"
		} else {
			reason = exitStatus(code).Error()
		}
		if err := a.closeAfterExit(ctx, b, runner, branch, reason, lease.until, signals); err != nil && code == 0 {
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

type renewal struct {
	closed bool
	until  time.Time
}

func (a *app) renew(ctx context.Context, runID string, every time.Duration, leaseUntil time.Time) renewal {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return renewal{until: leaseUntil}
		case <-ticker.C:
			var result flow.Result
			err := a.client.Post(ctx, "/v1/runs/"+runID+"/heartbeat", "", api.ReportRequest{}, &result)
			if flow.CodeOf(err) == flow.CodeConflict {
				fmt.Fprintf(a.errOut, "metronome run: run %s is closed; stopped renewing: %v\n", runID, err)
				return renewal{closed: true, until: leaseUntil}
			}
			if err != nil && ctx.Err() == nil {
				fmt.Fprintf(a.errOut, "metronome run: renew %s: %v\n", runID, err)
			}
			if err == nil {
				for _, event := range result.Events {
					if event.Type != flow.EvHeartbeat {
						continue
					}
					var heartbeat struct {
						LeaseUntil time.Time `json:"lease_until"`
					}
					if json.Unmarshal(event.Data, &heartbeat) == nil && !heartbeat.LeaseUntil.IsZero() {
						leaseUntil = heartbeat.LeaseUntil
					}
				}
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

func waitDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type runClose struct {
	runID, action, target, wanted, message string
	body                                   any
}

func newRunClose(runID, branch, reason string) runClose {
	if branch != "" {
		return runClose{runID: runID, action: "submit", target: runID + " on " + branch, wanted: "submit on " + branch,
			message: fmt.Sprintf("submitted %s on %s", runID, branch), body: api.SubmitRequest{Branch: branch}}
	}
	return runClose{runID: runID, action: "release", target: runID, wanted: "drop", message: fmt.Sprintf("dropped %s (%s)", runID, reason)}
}

func (a *app) closeAttempt(ctx context.Context, close runClose, requestID string) error {
	ctx, cancel := context.WithTimeout(ctx, closeAttemptTimeout)
	defer cancel()
	err := a.client.Post(ctx, "/v1/runs/"+close.runID+"/"+close.action, requestID, close.body, nil)
	if err == nil {
		fmt.Fprintln(a.errOut, "metronome run: "+close.message)
	}
	return err
}

func (a *app) reportCloseError(close runClose, err error) error {
	if flow.CodeOf(err) == flow.CodeConflict {
		fmt.Fprintf(a.errOut, "metronome run: run %s was already closed (wanted to %s): %v\n", close.runID, close.wanted, err)
		return nil
	}
	if err != nil {
		fmt.Fprintf(a.errOut, "metronome run: could not %s run %s: %v\n", close.action, close.target, err)
	}
	return err
}

func (a *app) closeRun(ctx context.Context, runID, branch, reason string) error {
	close := newRunClose(runID, branch, reason)
	return a.reportCloseError(close, a.closeAttempt(context.WithoutCancel(ctx), close, ""))
}

func (a *app) closeAfterExit(ctx context.Context, b brief, runner, branch, reason string, leaseUntil time.Time, signals <-chan os.Signal) error {
	select {
	case <-signals:
	default:
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() {
		cancel()
		<-watching
	}()
	operation := newRunClose(b.Claim.RunID, branch, reason)
	requestID := "metronome-run-" + b.Claim.RunID + "-close"
	started := a.now()
	leaseStop := leaseUntil.Add(closeLeaseGrace)
	stop := started.Add(closeBound)
	if leaseStop.Before(stop) {
		stop = leaseStop
	}
	var err error
	for delay := closeInitialDelay; ; delay = min(delay*2, closeMaxDelay) {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		err = a.closeAttempt(ctx, operation, requestID)
		if err == nil {
			return nil
		}
		var status *api.StatusError
		if errors.As(err, &status) && status.Code < http.StatusInternalServerError {
			if flow.CodeOf(err) == flow.CodeConflict {
				readCtx, stopRead := context.WithTimeout(ctx, closeAttemptTimeout)
				var run flow.Run
				readErr := a.client.Get(readCtx, "/v1/runs/"+b.Claim.RunID, &run)
				stopRead()
				if readErr == nil && run.ClosedReason == flow.CloseLeaseExpired {
					a.reportExpired(b, runner, branch, run.LeaseUntil)
					if branch == "" {
						return nil
					}
					return err
				}
			}
			return a.reportCloseError(operation, err)
		}
		remaining := stop.Sub(a.now())
		if remaining <= 0 || ctx.Err() != nil {
			fmt.Fprintf(a.errOut, "metronome run: could not %s run %s: %v\n", operation.action, operation.target, err)
			break
		}
		wait := min(delay, remaining)
		fmt.Fprintf(a.errOut, "metronome run: could not %s run %s: %v; retrying in %s\n", operation.action, operation.target, err, wait)
		if waitErr := a.wait(ctx, wait); waitErr != nil {
			err = waitErr
			break
		}
	}
	if !a.now().Before(leaseStop) {
		a.reportExpired(b, runner, branch, leaseUntil)
	} else {
		cause := "service unreachable"
		if ctx.Err() != nil {
			cause = "signal"
		}
		elapsed := a.now().Sub(started).Round(time.Second)
		deadline := leaseUntil.UTC().Format(time.RFC3339)
		if branch != "" {
			fmt.Fprintf(a.errOut, "metronome run: gave up submitting run %s on branch %s after %s (%s); until %s run metronome submit %s --branch %s, after that claim slice %s again and submit branch %s\n", b.Claim.RunID, branch, elapsed, cause, deadline, b.Claim.RunID, branch, b.Claim.SliceID, branch)
		} else {
			fmt.Fprintf(a.errOut, "metronome run: gave up dropping run %s after %s (%s); run metronome drop %s, or let its lease expire at %s\n", b.Claim.RunID, elapsed, cause, b.Claim.RunID, deadline)
		}
	}
	return err
}

func (a *app) reportExpired(b brief, runner, branch string, leaseUntil time.Time) {
	deadline := leaseUntil.UTC().Format(time.RFC3339)
	if branch != "" {
		fmt.Fprintf(a.errOut, "metronome run: the lease on run %s expired at %s before the service answered, so branch %s was not submitted; claim slice %s again (metronome next --runner %s --slice %s) and submit branch %s under the new run\n", b.Claim.RunID, deadline, branch, b.Claim.SliceID, runner, b.Claim.SliceID, branch)
	} else {
		fmt.Fprintf(a.errOut, "metronome run: the lease on run %s expired at %s before the service answered; the service will close it as lease_expired, which stalls the slice as a drop would\n", b.Claim.RunID, deadline)
	}
}
