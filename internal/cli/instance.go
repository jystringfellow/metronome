package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
	"github.com/jystringfellow/metronome/internal/instance"
	"github.com/jystringfellow/metronome/internal/supervise"
)

func (a *app) applyInstance(ctx context.Context, cfg instance.Config) error {
	var current []flow.WorkstreamView
	if err := a.client.Get(ctx, "/v1/workstreams", &current); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, ws := range current {
		known[ws.Name] = true
	}
	for _, ws := range cfg.Workstreams {
		path := "/v1/workstreams/" + ws.Name
		created, changed := false, false
		if !known[ws.Name] {
			err := a.client.Post(ctx, "/v1/workstreams", "", api.WorkstreamCreateRequest{Name: ws.Name, PartialLimits: ws.Limits}, nil)
			if err != nil {
				var status *api.StatusError
				if !errors.As(err, &status) || status.Code != http.StatusConflict {
					return err
				}
				if err := a.client.Get(ctx, "/v1/workstreams", &current); err != nil {
					return err
				}
				found := false
				for _, view := range current {
					if view.Name == ws.Name {
						found = true
						break
					}
				}
				if !found {
					return err
				}
			} else {
				created = true
			}
		}
		if !created && ws.Limits != (flow.PartialLimits{}) {
			var result flow.Result
			if err := a.client.Post(ctx, path+"/limits", "", ws.Limits, &result); err != nil {
				return err
			}
			changed = len(result.Events) > 0
		}
		labelChanged := false
		if ws.AdoptLabel != nil {
			var result flow.Result
			if err := a.client.Post(ctx, path+"/adopt-label", "", api.AdoptLabelRequest{Label: *ws.AdoptLabel}, &result); err != nil {
				return err
			}
			labelChanged = len(result.Events) > 0
		}
		switch {
		case created:
			fmt.Fprintf(a.out, "config: created workstream %s\n", ws.Name)
		case changed:
			fmt.Fprintf(a.out, "config: workstream %s limits set\n", ws.Name)
		case labelChanged:
			fmt.Fprintf(a.out, "config: workstream %s adopt label set\n", ws.Name)
		default:
			fmt.Fprintf(a.out, "config: workstream %s unchanged\n", ws.Name)
		}
	}
	return nil
}

func synchronizedOutputs(out, errOut io.Writer) (io.Writer, io.Writer) {
	mutex := &sync.Mutex{}
	return lockedOutput{mu: mutex, w: out}, lockedOutput{mu: mutex, w: errOut}
}

type prefixedOutput struct {
	writer  io.Writer
	prefix  string
	pending bytes.Buffer
}

func (w *prefixedOutput) Write(data []byte) (int, error) {
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		w.pending.Write(line)
		if bytes.HasSuffix(line, []byte("\n")) {
			if _, err := fmt.Fprintf(w.writer, "%s: %s", w.prefix, w.pending.String()); err != nil {
				return 0, err
			}
			w.pending.Reset()
		}
	}
	return len(data), nil
}

func (w *prefixedOutput) flush() {
	if w.pending.Len() > 0 {
		fmt.Fprintf(w.writer, "%s: %s\n", w.prefix, w.pending.String())
		w.pending.Reset()
	}
}

func (a *app) instanceLoops(cfg instance.Config) []supervise.Loop {
	var loops []supervise.Loop
	add := func(name string, schedule instance.Schedule, round func(context.Context, *app) error) {
		local := *a
		client := *a.client
		client.Actor = schedule.Actor
		local.client = &client
		out := &prefixedOutput{writer: a.out, prefix: name}
		errOut := &prefixedOutput{writer: a.errOut, prefix: name}
		local.out, local.errOut = out, errOut
		loops = append(loops, supervise.Loop{Name: name, Every: schedule.Interval(), Round: func(ctx context.Context) error {
			defer out.flush()
			defer errOut.flush()
			return round(ctx, &local)
		}})
	}
	for _, ws := range cfg.Workstreams {
		if ws.Intake != nil {
			add("intake "+ws.Name, ws.Intake.Schedule, func(ctx context.Context, local *app) error {
				if err := local.intakeOnce(ctx, ws.Name, ws.Intake.Command); err != nil {
					return fmt.Errorf("%w; nothing synced", err)
				}
				return nil
			})
		}
		for _, pub := range ws.Publish {
			add("publish "+ws.Name+"/"+pub.Name, pub.Schedule, func(ctx context.Context, local *app) error {
				return local.publishOnce(ctx, ws.Name, pub.Name, pub.BatchSize(), pub.Command.Command)
			})
		}
	}
	if poll := cfg.GitHubPoll; poll != nil {
		add("github poll", poll.Schedule, func(ctx context.Context, local *app) error {
			return local.pollOnce(ctx, github.Poller{Client: local.client, Source: local.prs, Adopt: local.pullRequests, AdoptRepos: poll.AdoptRepos})
		})
	}
	return loops
}
