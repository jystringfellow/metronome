package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestIntakeSyncsFromACommand(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "workstream", "create", "example")
	items := []flow.BacklogItem{{Ref: "ACME-1", Title: "first", Owner: "pat"}, {Ref: "ACME-2", Title: "second", Owner: "pat"}, {Ref: "ACME-3", Title: "third", Owner: "pat"}}
	source := e.file("source.json", api.BacklogRequest{Items: items})
	copyPath := filepath.Join(e.dir, "snapshot.json")
	args := []string{"intake", "--workstream", "example", "--", "sh", "-c", `echo reading >&2; test "$METRONOME_WORKSTREAM" = example && cp "$METRONOME_ITEMS" "$1" && cat "$2"`, "sh", copyPath, source}
	if out := e.ok("pat", args...); !strings.Contains(out, "reading") || strings.Count(out, "item_added") != 3 {
		t.Fatal(out)
	}
	data, err := os.ReadFile(copyPath)
	if err != nil || !strings.Contains(string(data), `"workstream":"example"`) {
		t.Fatal(string(data), err)
	}
	if out := e.ok("pat", args...); !strings.Contains(out, "no change") {
		t.Fatal(out)
	}
	data, err = os.ReadFile(copyPath)
	if err != nil || !strings.Contains(string(data), `"ref":"ACME-1","id":"I1","status":"ready"`) {
		t.Fatal(string(data), err)
	}
	items[1].Title, items[1].Intent = "new second", "new intent"
	e.file("source.json", api.BacklogRequest{Items: []flow.BacklogItem{items[1], items[0]}})
	if out := e.ok("pat", args...); strings.Count(out, "item_updated") != 2 || !strings.Contains(out, "item_shelved") {
		t.Fatal(out)
	}
	b := e.board()
	if b.Items[1].Rank != 1 || b.Items[1].Title != "new second" || b.Items[1].Intent != "new intent" || b.Items[2].Status != flow.ItemShelved {
		t.Fatal(b.Items)
	}
	if out := e.ok("pat", "board"); strings.Contains(out, "third") {
		t.Fatal(out)
	}
}

func TestIntakeFailuresSyncNothing(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-1","title":"first","owner":"pat"}]}`)
	before := e.board()
	for _, test := range []struct {
		name, message string
		args          []string
	}{
		{"exit", "command exited 3", []string{"sh", "-c", `echo '{"items":[]}'; exit 3`}},
		{"invalid JSON", "not a backlog", []string{"echo", "{"}},
		{"missing array", "items array", []string{"echo", `{}`}},
		{"null array", "items array", []string{"echo", `{"items":null}`}},
		{"unknown field", "unknown field", []string{"echo", `{"items":[],"extra":true}`}},
		{"multiple values", "exactly one JSON value", []string{"echo", `{"items":[]} {}`}},
		{"wrong workstream", "disagree", []string{"echo", `{"workstream":"example","items":[]}`}},
		{"invalid item", "invalid", []string{"echo", `{"items":[{"ref":"ACME-2"}]}`}},
		{"missing command", "command failed", []string{filepath.Join(e.dir, "missing-command")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, code := e.run("pat", append([]string{"intake", "--workstream", "default", "--"}, test.args...)...)
			if code != 1 || !strings.Contains(out, test.message) || !strings.Contains(out, "nothing synced") {
				t.Fatal(out, code)
			}
			if !reflect.DeepEqual(before, e.board()) {
				t.Fatal("failed round changed board")
			}
		})
	}
	if out, code := e.run("pat", "intake", "--workstream", "missing", "--", "echo", `{"items":[]}`); code != 1 || !strings.Contains(out, "not_found") || !strings.Contains(out, "nothing synced") {
		t.Fatal(out, code)
	}
}

func TestIntakeUsage(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"intake", "--workstream", "default", "echo"},
		{"intake", "--workstream", "default", "--"},
		{"intake", "--", "echo"},
		{"intake", "--workstream", "default", "--every", "-1s", "--", "echo"},
		{"--request-id", "example", "intake", "--workstream", "default", "--", "echo"},
	} {
		if out, code := e.run("pat", args...); code != 2 {
			t.Fatal(out, code)
		}
	}
}

func TestIntakeLoopKeepsGoingAfterFailures(t *testing.T) {
	e := newEnv(t)
	source := e.file("source.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "first", Owner: "pat"}}})
	counter := filepath.Join(e.dir, "counter")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.RunWithHTTP(ctx, []string{"--server", "http://example.com", "--as", "pat", "intake", "--workstream", "default", "--every", "10ms", "--", "sh", "-c", `
round=0
if [ -f "$1" ]; then round=$(cat "$1"); fi
round=$((round + 1))
printf '%s' "$round" > "$1"
case "$round" in
  1) exit 1 ;;
  2) echo '{' ;;
  *) cat "$2" ;;
esac`, "sh", counter, source}, &out, &errOut, e.http)
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case <-ctx.Done():
			waiting = false
		case <-ticker.C:
			data, err := os.ReadFile(counter)
			round, parseErr := strconv.Atoi(string(data))
			if err == nil && parseErr == nil && round >= 5 {
				waiting = false
			}
		}
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatal(code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "command exited 1") || !strings.Contains(errOut.String(), "not a backlog") || strings.Count(out.String(), "item_added") != 1 || !strings.Contains(out.String(), "no change") {
		t.Fatal(out.String(), errOut.String())
	}
	if b := e.board(); len(b.Items) != 1 || b.Items[0].Ref != "ACME-1" {
		t.Fatal(b.Items)
	}
}

func TestIntakeReplaysFromSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.db")
	writeStore, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writeStore.Close() })
	e := newEnvWithStore(t, writeStore)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-1","title":"first","owner":"pat"},{"ref":"ACME-2","title":"second","owner":"pat"}]}`)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-2","title":"retitled","owner":"pat"}]}`)
	before := e.board()
	if err := writeStore.Close(); err != nil {
		t.Fatal(err)
	}
	readStore, err := store.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readStore.Close()
	replayed := newEnvWithStore(t, readStore)
	if after := replayed.board(); !reflect.DeepEqual(before, after) {
		t.Fatalf("replayed board = %+v, want %+v", after, before)
	}
}

func TestIntakeAPIRequiresAnArrayAndRespectsClaims(t *testing.T) {
	e := newEnv(t)
	c := api.Client{BaseURL: "http://example.com", Actor: "pat", HTTP: e.http}
	for _, body := range []string{`{"workstream":"default"}`, `{"workstream":"default","items":null}`, `{"items":[]}`} {
		var result flow.Result
		if err := c.Post(context.Background(), "/v1/backlog/intake", "", json.RawMessage(body), &result); flow.CodeOf(err) != flow.CodeInvalid {
			t.Fatal(err)
		}
	}
	items := []flow.BacklogItem{{Ref: "ACME-1", Title: "claimed", Owner: "pat", ClaimedBy: "laptop-b"}}
	var result flow.Result
	if err := c.Post(context.Background(), "/v1/backlog/intake", "", api.IntakeRequest{Workstream: "default", Items: &items}, &result); err != nil || len(result.Events) != 0 || len(result.Notes) != 1 {
		t.Fatal(result, err)
	}
	file := e.file("claimed.json", api.BacklogRequest{Items: items})
	if out := e.ok("pat", "backlog", "sync", file); !strings.Contains(out, "note ACME-1 is claimed by laptop-b") {
		t.Fatal(out)
	}
	if len(e.board().Items) != 0 {
		t.Fatal("claimed item added")
	}
}
