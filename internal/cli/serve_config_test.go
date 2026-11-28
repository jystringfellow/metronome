package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

type serveOutput struct {
	io.Writer
	started chan string
}

func (out serveOutput) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "metronome serving on ") {
		out.started <- strings.Fields(string(data))[3]
	}
	return out.Writer.Write(data)
}

func TestServeConfig(t *testing.T) {
	dir := t.TempDir()
	db, config := filepath.Join(dir, "metronome.db"), filepath.Join(dir, "instance.json")
	counter, published := filepath.Join(dir, "counter"), filepath.Join(dir, "published")
	t.Setenv("METRONOME_TEST_SECRET", "s3cret")
	writeConfig := func(text string) {
		t.Helper()
		if err := os.WriteFile(config, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := []string{"sh", "-c", `if [ ! -f "$1" ]; then touch "$1"; exit 1; fi; test "$METRONOME_TEST_SECRET" = s3cret || exit 2; echo '{"items":[{"ref":"ACME-1","title":"first","owner":"pat"}]}'`, "sh", counter}
	intake, _ := json.Marshal(command)
	publish, _ := json.Marshal([]string{"sh", "-c", `cat >> "$1"`, "sh", published})
	writeConfig(fmt.Sprintf(`{"version":1,"actor":"pat","workstreams":[{"name":"example","limits":{"execute":2},"intake":{"every":"10ms","command":%s},"publish":[{"name":"board","every":"10ms","command":%s}]}],"github_poll":{"every":"10ms"}}`, intake, publish))
	start := func(check func(context.Context, *api.Client)) (string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var out, errOut bytes.Buffer
		started := make(chan string, 1)
		done := make(chan int, 1)
		go func() {
			done <- cli.RunWithDeps(ctx, []string{"--as", "pat", "serve", "--addr", "127.0.0.1:0", "--db", db, "--config", config, "--tick", "1h"}, serveOutput{&out, started}, &errOut, cli.Deps{GitHub: branchPRs{}})
		}()
		func() {
			defer func() {
				cancel()
				if code := <-done; code != 0 {
					t.Errorf("serve: %d: %s", code, errOut.String())
				}
				if t.Failed() {
					t.Logf("serve logs: %.2000s\n%.2000s", out.String(), errOut.String())
				}
			}()
			select {
			case address := <-started:
				check(ctx, &api.Client{BaseURL: address, Actor: "pat"})
			case <-ctx.Done():
				t.Error("serve did not start")
			}
		}()
		return out.String(), errOut.String()
	}
	waitFor := func(ctx context.Context, condition func() bool) {
		t.Helper()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatal("condition not met")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	out, errOut := start(func(ctx context.Context, client *api.Client) {
		waitFor(ctx, func() bool {
			var board api.Board
			return client.Get(ctx, "/v1/board?workstream=example", &board) == nil && len(board.Items) == 1
		})
		if err := client.Post(ctx, "/v1/plan/start", "", api.PlanStartRequest{Workstream: "example"}, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(ctx, func() bool {
			data, _ := os.ReadFile(published)
			return strings.Contains(string(data), `"ref":"ACME-1"`) && strings.Contains(string(data), `"transition":"planning"`)
		})
		limit := 7
		if err := client.Post(ctx, "/v1/workstreams/example/limits", "", flow.PartialLimits{Plan: &limit}, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(errOut, "intake example: round 1 failed") || !strings.Contains(out, "intake example: round 2 ok") || !strings.Contains(out, "github poll: round 1 ok") || strings.Contains(out+errOut, "s3cret") {
		t.Fatal(out, errOut)
	}
	writeConfig(`{"version":1,"actor":"pat","workstreams":[{"name":"example","limits":{"execute":3}}]}`)
	var lastSeq int64
	check := func(ctx context.Context, client *api.Client) {
		waitFor(ctx, func() bool {
			var views []flow.WorkstreamView
			if client.Get(ctx, "/v1/workstreams", &views) != nil {
				return false
			}
			for _, view := range views {
				if view.Name == "example" && view.Limits.Execute == 3 && view.Limits.Plan == 7 {
					return true
				}
			}
			return false
		})
		var ready struct {
			LastSeq int64 `json:"last_seq"`
		}
		if err := client.Get(ctx, "/readyz", &ready); err != nil {
			t.Fatal(err)
		}
		if lastSeq != 0 && ready.LastSeq != lastSeq {
			t.Fatalf("restart changed event sequence: %d -> %d", lastSeq, ready.LastSeq)
		}
		lastSeq = ready.LastSeq
	}
	start(check)
	start(check)
}

func TestServeInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	config, db := filepath.Join(dir, "config.json"), filepath.Join(dir, "metronome.db")
	if err := os.WriteFile(config, []byte(`{"version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := cli.Run(t.Context(), []string{"serve", "--config", config, "--db", db}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "version") {
		t.Fatal(code, errOut.String())
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("database must not exist: %v", err)
	}
}

func TestServeConfigApplyFailure(t *testing.T) {
	dir := t.TempDir()
	config, db := filepath.Join(dir, "config.json"), filepath.Join(dir, "metronome.db")
	st, err := store.OpenSQLite(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := flow.NewEngine(t.Context(), flow.DefaultConfig(), st, nil)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	_, err = eng.SetAdoptLabel(t.Context(), "pat", "", "default", "adopt")
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"version":1,"actor":"pat","workstreams":[{"name":"example","adopt_label":"adopt","intake":{"every":"1ms","command":["echo","{\"items\":[]}"]}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if code := cli.Run(ctx, []string{"serve", "--config", config, "--db", db, "--addr", "127.0.0.1:0"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "already used") || strings.Contains(out.String()+errOut.String(), "round 1") {
		t.Fatal(code, out.String(), errOut.String())
	}
}
