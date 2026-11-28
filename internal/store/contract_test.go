package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
	"github.com/jystringfellow/metronome/internal/store/pgtest"
)

type contractStore interface {
	flow.Store
	Ping(context.Context) error
}

type storeFactory func(*testing.T) (contractStore, func() contractStore)

func TestContract(t *testing.T) {
	factories := map[string]storeFactory{
		"memory": func(t *testing.T) (contractStore, func() contractStore) {
			return store.NewMemory(), nil
		},
		"sqlite": func(t *testing.T) (contractStore, func() contractStore) {
			path := filepath.Join(t.TempDir(), "metronome.db")
			var current *store.SQLite
			open := func() contractStore {
				if current != nil {
					current.Close()
				}
				var err error
				current, err = store.OpenSQLite(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				return current
			}
			t.Cleanup(func() {
				if current != nil {
					current.Close()
				}
			})
			return open(), open
		},
		"postgres": func(t *testing.T) (contractStore, func() contractStore) {
			url := pgtest.URL(t)
			var current *store.Postgres
			open := func() contractStore {
				if current != nil {
					current.Close()
				}
				var err error
				current, err = store.OpenPostgres(t.Context(), url)
				if err != nil {
					t.Fatal(err)
				}
				return current
			}
			t.Cleanup(func() {
				if current != nil {
					current.Close()
				}
			})
			return open(), open
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			t.Run("append", func(t *testing.T) { contractAppend(t, factory) })
			t.Run("replay", func(t *testing.T) { contractReplay(t, factory) })
		})
	}
}

func contractAppend(t *testing.T, factory storeFactory) {
	ctx := t.Context()
	s, reopen := factory(t)
	at := time.Date(2026, 11, 26, 9, 0, 0, 123, time.FixedZone("example", 3600))
	input := []flow.Event{
		{ID: "a/0", Type: "example", At: at, Actor: "pat", Data: json.RawMessage(`{"text": "é\u0000", "value": 1}`)},
		{ID: "a/1", Type: "example", At: at, Actor: "pat", Data: json.RawMessage(`{"item_id":"I1"}`)},
	}
	out, err := s.Append(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Seq != 1 || out[1].Seq != 2 || out[0].At.Location() != time.UTC || !out[0].At.Equal(at) {
		t.Fatalf("append = %+v", out)
	}
	next := flow.Event{ID: "b/0", Type: "example", At: at, Data: json.RawMessage(`{}`)}
	for _, batch := range [][]flow.Event{{next, input[0]}, {next, next}} {
		if _, err := s.Append(ctx, batch); !errors.Is(err, store.ErrDuplicateID) {
			t.Fatalf("duplicate append error = %v", err)
		}
	}
	if empty, err := s.Append(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty append = %v, %v", empty, err)
	}
	last, err := s.Append(ctx, []flow.Event{next})
	if err != nil || len(last) != 1 || last[0].Seq != 3 {
		t.Fatalf("next append = %v, %v", last, err)
	}
	out = append(out, last...)
	if reopen != nil {
		s = reopen()
	}
	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, out) || string(loaded[0].Data) != string(input[0].Data) || loaded[0].Type != input[0].Type || loaded[0].Actor != input[0].Actor {
		t.Fatalf("loaded = %+v, want %+v", loaded, out)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func contractReplay(t *testing.T, factory storeFactory) {
	ctx := t.Context()
	s, reopen := factory(t)
	clock := func() time.Time { return time.Date(2026, 11, 26, 9, 0, 0, 123, time.UTC) }
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), s, clock)
	if err != nil {
		t.Fatal(err)
	}
	check := func(result flow.Result, err error) flow.Result {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	check(eng.CreateWorkstream(ctx, "pat", "create", "example", flow.PartialLimits{}))
	items := []flow.BacklogItem{{Ref: "ACME-1", Title: "example", Owner: "pat"}}
	first := check(eng.SyncBacklog(ctx, "pat", "sync", "example", items))
	check(eng.StartPlan(ctx, "pat", "start", "example", "I1"))
	check(eng.ProposePlan(ctx, "pat", "propose", "I1", []flow.SliceSpec{{Title: "export", Repo: "acme/app"}}, ""))
	check(eng.ApprovePlan(ctx, "pat", "approve", "I1", nil))
	check(eng.Claim(ctx, "pat", "claim", "runner", "example", ""))
	check(eng.RaiseFlag(ctx, "pat", "ask", flow.Subject{Kind: flow.SubjectItem, ID: "I1"}, flow.AskAnswer, "Which format?", "pat"))
	if reopen != nil {
		s = reopen()
	}
	replayed, err := flow.NewEngine(ctx, flow.DefaultConfig(), s, clock)
	if err != nil {
		t.Fatal(err)
	}
	eng.View(func(state *flow.State) {
		replayed.View(func(other *flow.State) {
			if !reflect.DeepEqual(state, other) {
				t.Error("replayed state differs")
			}
		})
	})
	if !reflect.DeepEqual(eng.EventsAfter(0, 0), replayed.EventsAfter(0, 0)) {
		t.Fatal("replayed events differ")
	}
	duplicate := check(replayed.SyncBacklog(ctx, "pat", "sync", "example", items))
	if !duplicate.Duplicate || !reflect.DeepEqual(first.Events, duplicate.Events) || eng.LastSeq() != replayed.LastSeq() {
		t.Fatalf("idempotency after replay: %+v", duplicate)
	}
}
