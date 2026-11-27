package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

func (a *app) publish(ctx context.Context, args []string) error {
	separator := slices.Index(args, "--")
	if separator < 0 || separator == len(args)-1 {
		return usagef("give -- CMD [ARGS...]")
	}
	fs := newFlags("publish")
	ws := fs.String("workstream", "", "")
	name := fs.String("name", "", "")
	every := fs.Duration("every", 0, "")
	limit := fs.Int("batch", 100, "")
	if err := parse(fs, args[:separator], 0); err != nil {
		return err
	}
	if *ws == "" || *name == "" {
		return usagef("--workstream and --name are required")
	}
	if *every < 0 || *limit < 1 || *limit > 1000 || a.requestID != "" {
		return usagef("invalid --every, --batch or --request-id")
	}
	round := func(ctx context.Context) error { return a.publishOnce(ctx, *ws, *name, *limit, args[separator+1:]) }
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *every == 0 {
		return round(ctx)
	}
	ticker := time.NewTicker(*every)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := round(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(a.errOut, "publish: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

func (a *app) publishOnce(ctx context.Context, ws, name string, limit int, args []string) error {
	path := "/v1/workstreams/" + url.PathEscape(ws) + "/publishers/" + url.PathEscape(name)
	for {
		var batch api.PublishBatch
		if err := a.client.Get(ctx, fmt.Sprintf("%s/batch?limit=%d", path, limit), &batch); err != nil {
			return err
		}
		if len(batch.Transitions) == 0 {
			return nil
		}
		if err := a.publishBatch(ctx, path, batch, args); err != nil {
			return err
		}
		if !batch.More {
			return nil
		}
	}
}

func (a *app) publishBatch(ctx context.Context, path string, batch api.PublishBatch, args []string) error {
	dir, err := os.MkdirTemp("", "metronome-publisher-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "state.json")
	initial := batch.State
	if initial == nil {
		initial = json.RawMessage("null")
	}
	if err := os.WriteFile(file, initial, 0600); err != nil {
		return err
	}
	var input bytes.Buffer
	enc := json.NewEncoder(&input)
	for _, transition := range batch.Transitions {
		if err := enc.Encode(transition); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = &input, a.out, a.errOut
	cmd.Env = append(os.Environ(), "METRONOME_WORKSTREAM="+batch.Workstream, "METRONOME_PUBLISHER="+batch.Publisher, "METRONOME_PUBLISHER_STATE="+file)
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	state, err := os.ReadFile(file)
	var compact bytes.Buffer
	if err == nil && (len(state) > flow.MaxPublisherState || json.Compact(&compact, state) != nil) {
		err = fmt.Errorf("invalid publisher state")
	}
	req := api.PublisherAdvanceRequest{After: batch.Cursor, Seq: batch.Cursor}
	if err == nil && !bytes.Equal(compact.Bytes(), initial) {
		req.State = compact.Bytes()
	}
	if runErr == nil && err == nil {
		req.Seq = batch.Transitions[len(batch.Transitions)-1].Seq
	}
	if req.Seq != req.After || req.State != nil {
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if advanceErr := a.client.Post(commitCtx, path+"/advance", "", req, nil); advanceErr != nil {
			return advanceErr
		}
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return fmt.Errorf("command exited %d; cursor stays at %d; the same transitions will be offered again", exit.ExitCode(), batch.Cursor)
		}
		return fmt.Errorf("command failed: %w; cursor stays at %d; the same transitions will be offered again", runErr, batch.Cursor)
	}
	if err != nil {
		return fmt.Errorf("%w; cursor stays at %d", err, batch.Cursor)
	}
	fmt.Fprintf(a.out, "published %d transition(s) through seq %d\n", len(batch.Transitions), req.Seq)
	return nil
}
