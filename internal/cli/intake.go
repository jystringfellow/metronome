package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

type intakeItem struct {
	Ref    string          `json:"ref"`
	ID     string          `json:"id"`
	Status flow.ItemStatus `json:"status"`
}

type intakeSnapshot struct {
	Workstream string       `json:"workstream"`
	Items      []intakeItem `json:"items"`
}

func (a *app) intake(ctx context.Context, args []string) error {
	separator := slices.Index(args, "--")
	if separator < 0 || separator == len(args)-1 {
		return usagef("give -- CMD [ARGS...]")
	}
	fs := newFlags("intake")
	ws := fs.String("workstream", "", "")
	every := fs.Duration("every", 0, "")
	if err := parse(fs, args[:separator], 0); err != nil {
		return err
	}
	if *ws == "" {
		return usagef("--workstream NAME is required")
	}
	if *every < 0 {
		return usagef("--every must not be negative")
	}
	if a.requestID != "" {
		return usagef("intake does not accept --request-id")
	}
	command := args[separator+1:]
	round := func(ctx context.Context) error {
		if err := a.intakeOnce(ctx, *ws, command); err != nil {
			return fmt.Errorf("%w; nothing synced", err)
		}
		return nil
	}
	if *every == 0 {
		return round(ctx)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*every)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := round(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(a.errOut, "intake: %s\n", a.redact(err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

func (a *app) intakeOnce(ctx context.Context, ws string, args []string) error {
	var board api.Board
	if err := a.client.Get(ctx, "/v1/board?workstream="+url.QueryEscape(ws), &board); err != nil {
		return err
	}
	snapshot := intakeSnapshot{Workstream: ws, Items: make([]intakeItem, 0, len(board.Items))}
	for _, it := range board.Items {
		snapshot.Items = append(snapshot.Items, intakeItem{Ref: it.Ref, ID: it.ID, Status: it.Status})
	}
	file, err := os.CreateTemp("", "metronome-items-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(snapshot)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env, err = a.childEnv(ctx, "METRONOME_WORKSTREAM="+ws, "METRONOME_ITEMS="+file.Name())
	if err != nil {
		return err
	}
	stderr := &redactingOutput{writer: a.errOut, redact: a.redact}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	flushErr := stderr.flush()
	if a.holds(stdout.Bytes()) {
		return fmt.Errorf("command output contains the GitHub App token")
	}
	if flushErr != nil {
		return flushErr
	}
	if err := runErr; err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("command exited %d", exit.ExitCode())
		}
		return fmt.Errorf("command failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	req, err := decodeIntake(&stdout, ws)
	if err != nil {
		return fmt.Errorf("command output is not a backlog: %w", err)
	}
	return a.post(ctx, "/v1/backlog/intake", req)
}

func decodeIntake(reader io.Reader, ws string) (api.IntakeRequest, error) {
	var req api.IntakeRequest
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return req, fmt.Errorf("expected exactly one JSON value")
	}
	if req.Items == nil {
		return req, fmt.Errorf("intake needs an items array")
	}
	if req.Workstream != "" && req.Workstream != ws {
		return req, fmt.Errorf("workstream flag and command output disagree")
	}
	req.Workstream = ws
	return req, nil
}
