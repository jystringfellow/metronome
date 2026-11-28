package store_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestBackupUnderConcurrentWrites(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	src, dest := filepath.Join(dir, "source.db"), filepath.Join(dir, "backup.db")
	db, err := store.OpenSQLite(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	started, stop := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		remaining := 10
		for batch := 0; ; batch++ {
			events := make([]flow.Event, 3)
			for index := range events {
				events[index] = flow.Event{ID: fmt.Sprintf("w%d/%d", batch, index), Type: "example", At: time.Now().UTC(), Actor: "pat", Data: []byte(`{"example":true}`)}
			}
			if _, err := db.Append(ctx, events); err != nil {
				done <- err
				return
			}
			if batch == 9 {
				close(started)
			}
			select {
			case <-stop:
				remaining--
				if remaining == 0 {
					done <- nil
					return
				}
			default:
			}
		}
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatal(err)
	}
	result, backupErr := store.Backup(ctx, src, dest)
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if backupErr != nil {
		t.Fatal(backupErr)
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
	count := len(snapshot)
	if count < 30 || count%3 != 0 || count >= len(all) || !reflect.DeepEqual(snapshot, all[:count]) || result.Events != int64(count) || result.LastSeq != snapshot[count-1].Seq {
		t.Fatalf("snapshot count=%d source count=%d result=%+v", count, len(all), result)
	}
}

func TestBackupRefusals(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	src, dest := filepath.Join(dir, "source.db"), filepath.Join(dir, "backup.db")
	db, err := store.OpenSQLite(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := os.WriteFile(dest, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Backup(ctx, src, dest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination: %v", err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "keep" {
		t.Fatalf("destination changed: %q %v", data, err)
	}
	missing, fresh := filepath.Join(dir, "missing.db"), filepath.Join(dir, "fresh.db")
	if _, err := store.Backup(ctx, missing, fresh); err == nil || !strings.Contains(err.Error(), "no database at") {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing source created: %v", err)
	}
	foreign := filepath.Join(dir, "foreign.db")
	other, err := sql.Open("sqlite", foreign)
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.ExecContext(ctx, "CREATE TABLE example (id INTEGER)")
	other.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Backup(ctx, foreign, fresh); err == nil || !strings.Contains(err.Error(), "no metronome event log") {
		t.Fatalf("foreign database: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") || entry.Name() == "fresh.db" {
			t.Fatalf("unexpected output %s", entry.Name())
		}
	}
}
