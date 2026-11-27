package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestPublisherCommandFailureResumeAndBatch(t *testing.T) {
	events := store.NewMemory()
	e := newEnvWithStore(t, events)
	e.ok("pat", "workstream", "create", "example")
	e.ok("pat", "intake", "--workstream", "example", "--", "echo", `{"items":[{"ref":"ACME-1","title":"first","owner":"pat","slices":[{"title":"first","repo":"acme/app"}]}]}`)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-2","title":"other","owner":"pat"}]}`)
	e.ok("pat", "plan", "start", "--workstream", "default")
	e.ok("pat", "plan", "start", "--workstream", "example")
	e.ok("pat", "plan", "approve", "I1")
	e.ok("pat", "next", "--runner", "laptop-a", "--workstream", "example")
	failed := filepath.Join(e.dir, "failed.jsonl")
	base := []string{"publish", "--workstream", "example", "--name", "board", "--batch", "2", "--"}
	output, code := e.run("pat", append(base, "sh", "-c", `cat > "$1"; echo '{"saved":true}' > "$METRONOME_PUBLISHER_STATE"; echo failed >&2; exit 3`, "sh", failed)...)
	if code != 1 || !strings.Contains(output, "offered again") || !strings.Contains(output, "failed") {
		t.Fatal(output, code)
	}
	first, err := os.ReadFile(failed)
	if err != nil {
		t.Fatal(err)
	}
	e = newEnvWithStore(t, events)
	result := filepath.Join(e.dir, "result.jsonl")
	counts := filepath.Join(e.dir, "counts")
	base = []string{"publish", "--workstream", "example", "--name", "board", "--batch", "2", "--"}
	output = e.ok("pat", append(base, "sh", "-c", `test "$METRONOME_WORKSTREAM" = example && test "$METRONOME_PUBLISHER" = board && test "$(cat "$METRONOME_PUBLISHER_STATE")" = '{"saved":true}' || exit 9; cat > "$2.batch"; wc -l < "$2.batch" >> "$2"; cat "$2.batch" >> "$1"`, "sh", result, counts)...)
	data, err := os.ReadFile(result)
	if err != nil || !strings.HasPrefix(string(data), string(first)) {
		t.Fatal(string(data), err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatal(lines)
	}
	for _, line := range lines {
		var transition flow.Transition
		if err := json.Unmarshal([]byte(line), &transition); err != nil || transition.Workstream != "example" || transition.Ref != "ACME-1" {
			t.Fatal(line, err)
		}
	}
	countData, err := os.ReadFile(counts)
	if err != nil || strings.TrimSpace(string(countData)) != "2\n2\n1" {
		t.Fatal(string(countData), err)
	}
	e.ok("pat", append(base, "sh", "-c", "exit 99")...)
	var batch api.PublishBatch
	client := api.Client{BaseURL: "http://example.com", Actor: "pat", HTTP: e.http}
	if err := client.Get(t.Context(), "/v1/workstreams/example/publishers/other/batch", &batch); err != nil || batch.Cursor != 0 || len(batch.Transitions) != 5 {
		t.Fatal(batch, err)
	}
}

func TestPublisherInvalidStateAndUsage(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-1","title":"first","owner":"pat"}]}`)
	e.ok("pat", "plan", "start")
	args := []string{"publish", "--workstream", "default", "--name", "board", "--"}
	if out, code := e.run("pat", append(args, "sh", "-c", `echo '{' > "$METRONOME_PUBLISHER_STATE"`)...); code != 1 || !strings.Contains(out, "invalid publisher state") {
		t.Fatal(out, code)
	}
	if out := e.ok("pat", append(args, "cat")...); !strings.Contains(out, `"transition":"planning"`) {
		t.Fatal(out)
	}
	for _, args := range [][]string{{"publish", "--", "cat"}, {"publish", "--workstream", "default", "--name", "board", "--batch", "0", "--", "cat"}, {"publish", "--workstream", "default", "--name", "board", "--every", "-1s", "--", "cat"}, {"publish", "--workstream", "default", "--name", "board"}, {"--request-id", "example", "publish", "--workstream", "default", "--name", "board", "--", "cat"}} {
		if out, code := e.run("pat", args...); code != 2 {
			t.Fatal(out, code)
		}
	}
}

type publisherTransport struct {
	underlying   http.RoundTripper
	afterAdvance func()
}

func (transport publisherTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	response, err := transport.underlying.RoundTrip(request)
	if err == nil && response.StatusCode == http.StatusOK && strings.HasSuffix(request.URL.Path, "/advance") && transport.afterAdvance != nil {
		transport.afterAdvance()
	}
	return response, err
}

func TestPublisherInterruptedRoundSavesState(t *testing.T) {
	for _, mode := range []struct {
		name     string
		flags    []string
		signal   string
		exitCode int
	}{
		{"one-shot", nil, "TERM", 1},
		{"periodic", []string{"--every", "1ms"}, "INT", 0},
	} {
		t.Run(mode.name, func(t *testing.T) {
			testPublisherInterrupt(t, mode.flags, mode.signal, mode.exitCode)
		})
	}
}

func testPublisherInterrupt(t *testing.T, flags []string, signalName string, wantExit int) {
	t.Helper()
	testSignals := make(chan os.Signal, 1)
	signal.Notify(testSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(testSignals)
	e := newEnv(t)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-1","title":"first","owner":"pat"}]}`)
	e.ok("pat", "plan", "start")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	transport := &http.Client{Transport: publisherTransport{underlying: e.http.Transport}}
	var output, stderr bytes.Buffer
	fifo := filepath.Join(e.dir, "wait")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--server", "http://example.com", "--as", "pat", "publish", "--workstream", "default", "--name", "board"}, flags...)
	args = append(args, "--", "sh", "-c", `cat >/dev/null; echo '{"saved":true}' > "$METRONOME_PUBLISHER_STATE"; kill -s "$1" "$PPID"; read line < "$2"`, "sh", signalName, fifo)
	if code := cli.RunWithHTTP(ctx, args, &output, &stderr, transport); code != wantExit || ctx.Err() != nil {
		t.Fatal(code, ctx.Err(), output.String(), stderr.String())
	}
	var batch api.PublishBatch
	client := api.Client{BaseURL: "http://example.com", Actor: "pat", HTTP: e.http}
	if err := client.Get(t.Context(), "/v1/workstreams/default/publishers/board/batch", &batch); err != nil || batch.Cursor != 0 || string(batch.State) != `{"saved":true}` || len(batch.Transitions) != 1 {
		t.Fatal(batch, err)
	}
}

func TestPublisherPeriodicRetryAndCancel(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "intake", "--workstream", "default", "--", "echo", `{"items":[{"ref":"ACME-1","title":"first","owner":"pat"}]}`)
	e.ok("pat", "plan", "start")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	transport := &http.Client{Transport: publisherTransport{underlying: e.http.Transport, afterAdvance: cancel}}
	var out, stderr bytes.Buffer
	first := filepath.Join(e.dir, "first.jsonl")
	second := filepath.Join(e.dir, "second.jsonl")
	args := []string{"--server", "http://example.com", "--as", "pat", "publish", "--workstream", "default", "--name", "board", "--every", "1ms", "--", "sh", "-c", `if test ! -f "$1"; then cat > "$1"; exit 3; fi; cat > "$2"; cmp "$1" "$2"`, "sh", first, second}
	if code := cli.RunWithHTTP(ctx, args, &out, &stderr, transport); code != 0 || !strings.Contains(stderr.String(), "publish: command exited 3") || !strings.Contains(out.String(), "published 1 transition(s)") {
		t.Fatal(code, out.String(), stderr.String())
	}
	var batch api.PublishBatch
	client := api.Client{BaseURL: "http://example.com", Actor: "pat", HTTP: e.http}
	if err := client.Get(t.Context(), "/v1/workstreams/default/publishers/board/batch", &batch); err != nil || batch.Cursor == 0 || len(batch.Transitions) != 0 {
		t.Fatal(batch, err)
	}
}
