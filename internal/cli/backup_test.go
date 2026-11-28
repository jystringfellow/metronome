package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestBackupAndRestoreFromTheCommandLine(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	src, dest := filepath.Join(dir, "source.db"), filepath.Join(dir, "backup.db")
	db, err := store.OpenSQLite(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := newEnvWithStore(t, db)
	client := &api.Client{BaseURL: "http://example.com", Actor: "pat", HTTP: e.http}
	started, stop := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for index := 0; ; index++ {
			name := fmt.Sprintf("example-%d", index)
			if err := client.Post(ctx, "/v1/workstreams", name+"-create", api.WorkstreamCreateRequest{Name: name}, nil); err != nil {
				done <- err
				return
			}
			if err := client.Post(ctx, "/v1/backlog/sync", name+"-sync", api.BacklogRequest{Workstream: name, Items: []flow.BacklogItem{{Ref: name, Title: "example", Owner: "pat"}}}, nil); err != nil {
				done <- err
				return
			}
			if index == 0 {
				close(started)
			}
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
		}
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := cli.Run(ctx, []string{"backup", "--db", src, "--to", dest}, &out, &errOut)
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("backup: %d %s %s", code, out.String(), errOut.String())
	}
	restored, err := store.OpenSQLite(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	snapshot, err := restored.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := db.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) < 2 || len(snapshot) > len(all) || !reflect.DeepEqual(snapshot, all[:len(snapshot)]) {
		t.Fatalf("backup is not a source prefix: %d of %d", len(snapshot), len(all))
	}
	if want := fmt.Sprintf("backed up %d events (last seq %d)", len(snapshot), snapshot[len(snapshot)-1].Seq); !strings.Contains(out.String(), want) {
		t.Fatal(out.String())
	}
	mem := store.NewMemory()
	if _, err := mem.Append(ctx, all[:len(snapshot)]); err != nil {
		t.Fatal(err)
	}
	if got, want := newEnvWithStore(t, restored).board(), newEnvWithStore(t, mem).board(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored board differs: %+v vs %+v", got, want)
	}
	if code := cli.Run(ctx, []string{"backup", "--db", src, "--to", dest}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "already exists") {
		t.Fatalf("overwrite: %d %s", code, errOut.String())
	}
}

func TestBackupUsage(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"backup", "--to", "backup.db", "extra"}} {
		if code := cli.Run(t.Context(), args, io.Discard, io.Discard); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
	var out bytes.Buffer
	if code := cli.Run(t.Context(), []string{"help"}, &out, io.Discard); code != 0 || !strings.Contains(out.String(), "backup --to FILE") {
		t.Fatalf("help: %d %s", code, out.String())
	}
}

func TestServeProbes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	reader, writer := io.Pipe()
	done := make(chan int, 1)
	var errOut bytes.Buffer
	path := filepath.Join(t.TempDir(), "metronome.db")
	go func() {
		code := cli.Run(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--db", path, "--tick", "1h"}, writer, &errOut)
		writer.Close()
		done <- code
	}()
	defer func() {
		reader.Close()
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("serve: %d %s", code, errOut.String())
		}
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(line)
	if len(fields) < 4 || !strings.HasPrefix(fields[3], "http://") {
		t.Fatal(line)
	}
	client := &http.Client{Timeout: time.Second}
	for {
		if ctx.Err() != nil {
			t.Fatal("readiness deadline exceeded")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fields[3]+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == 200 {
			if !strings.Contains(string(body), `"last_seq":0`) {
				t.Fatal(string(body))
			}
			break
		}
		if resp.StatusCode != 503 {
			t.Fatalf("readyz: %d %s", resp.StatusCode, body)
		}
	}
	resp, err := client.Get(fields[3] + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
}
