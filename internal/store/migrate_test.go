package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store/pgtest"
)

func TestReadSQLiteLogRefusesInvalidSources(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "source.db")
	if _, _, err := readSQLiteLog(ctx, path); err == nil || !strings.Contains(err.Error(), "no database") {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing source was created")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE example (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, _, err := readSQLiteLog(ctx, path); err == nil || !strings.Contains(err.Error(), "no metronome event log") {
		t.Fatalf("foreign source: %v", err)
	}
	s, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Append(ctx, []flow.Event{sampleEvent("one"), sampleEvent("two")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readSQLiteLog(ctx, path); err == nil || !strings.Contains(err.Error(), "gap at sequence 1") {
		t.Fatalf("gapped source: %v", err)
	}
}

func migrationSource(t *testing.T) (string, []flow.Event) {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "source.db")
	s, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), s, func() time.Time { return sampleEvent("one").At })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CreateWorkstream(ctx, "pat", "create", "example", flow.PartialLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SyncBacklog(ctx, "pat", "sync", "example", []flow.BacklogItem{{Title: "example", Ref: "ACME-1", Owner: "pat"}}); err != nil {
		t.Fatal(err)
	}
	events, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return path, events
}

func TestCompareReplayDetectsDifferentState(t *testing.T) {
	_, events := migrationSource(t)
	if err := compareReplay(t.Context(), events, events[:1]); err == nil || !strings.Contains(err.Error(), "replayed state differs") {
		t.Fatalf("comparator: %v", err)
	}
}

func TestPostgresCopySQLite(t *testing.T) {
	for _, mode := range []string{"empty", "prefix", "divergent"} {
		t.Run(mode, func(t *testing.T) {
			dst := testPostgres(t, pgtest.URL(t))
			path, events := migrationSource(t)
			var initial int64
			switch mode {
			case "prefix":
				if _, err := dst.Append(t.Context(), events[:1]); err != nil {
					t.Fatal(err)
				}
				initial = 1
			case "divergent":
				before, err := dst.Append(t.Context(), []flow.Event{sampleEvent("different")})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := CopySQLite(t.Context(), path, dst); err == nil || !strings.Contains(err.Error(), "do not match") {
					t.Fatalf("divergent migration: %v", err)
				}
				after, err := dst.Load(t.Context())
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("divergent destination was changed")
				}
				return
			}
			result, err := CopySQLite(t.Context(), path, dst)
			if err != nil {
				t.Fatal(err)
			}
			count := int64(len(events))
			if result != (CopyResult{Copied: count - initial, Events: count, LastSeq: count}) {
				t.Fatalf("copy result = %+v", result)
			}
			actual, err := dst.Load(t.Context())
			if err != nil || !reflect.DeepEqual(events, actual) {
				t.Fatalf("copied events differ: %v", err)
			}
			result, err = CopySQLite(t.Context(), path, dst)
			if err != nil || result.Copied != 0 || result.Events != count {
				t.Fatalf("rerun: %+v, %v", result, err)
			}
		})
	}
}

func TestPostgresCopySQLiteCancellation(t *testing.T) {
	url := pgtest.URL(t)
	dst := testPostgres(t, url)
	admin := adminConnection(t, url)
	path, events := migrationSource(t)
	if _, err := admin.Exec(t.Context(), `CREATE FUNCTION delay_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(3); RETURN NEW; END; $$;
		CREATE TRIGGER delay_event BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION delay_event()`); err != nil {
		t.Fatal("cannot install test delay trigger")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	_, err := CopySQLite(ctx, path, dst)
	cancel()
	if err == nil {
		t.Fatal("copy unexpectedly succeeded despite deadline")
	}
	actual, err := dst.Load(t.Context())
	if err != nil || len(actual) != 0 {
		t.Fatalf("cancelled copy left %d events: %v", len(actual), err)
	}
	if _, err := admin.Exec(t.Context(), `DROP TRIGGER delay_event ON events`); err != nil {
		t.Fatal("cannot remove test delay trigger")
	}
	result, err := CopySQLite(t.Context(), path, dst)
	if err != nil || result.Copied != int64(len(events)) {
		t.Fatalf("copy after cancellation: %+v, %v", result, err)
	}
}
