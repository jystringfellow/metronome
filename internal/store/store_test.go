package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestSQLiteAppendsLoadsAndRejectsDuplicateIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metronome.db")
	s, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 26, 9, 0, 0, 123, time.UTC)
	in := []flow.Event{
		{ID: "a/0", Type: flow.EvItemAdded, At: at, Actor: "test", Data: json.RawMessage(`{"item_id":"I1"}`)},
		{ID: "a/1", Type: flow.EvPlanStarted, At: at, Actor: "test", Data: json.RawMessage(`{"item_id":"I1"}`)},
	}
	out, err := s.Append(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Seq != 1 || out[1].Seq != 2 {
		t.Fatalf("seqs = %d, %d", out[0].Seq, out[1].Seq)
	}
	if _, err := s.Append(ctx, []flow.Event{{ID: "b/0", Type: "x", At: at, Data: json.RawMessage(`{}`)}, in[0]}); !errors.Is(err, store.ErrDuplicateID) {
		t.Fatalf("duplicate append error = %v", err)
	}
	s.Close()

	s, err = store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d events, want 2 (the rejected batch must not be partly written)", len(loaded))
	}
	if !loaded[0].At.Equal(at) || string(loaded[1].Data) != `{"item_id":"I1"}` {
		t.Fatalf("loaded = %+v", loaded)
	}
}
