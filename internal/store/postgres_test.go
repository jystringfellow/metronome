package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store/pgtest"
)

func testPostgres(t *testing.T, url string) *Postgres {
	t.Helper()
	s, err := OpenPostgres(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func adminConnection(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal("cannot connect to test database")
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func TestPostgresWriterLock(t *testing.T) {
	url := pgtest.URL(t)
	first := testPostgres(t, url)
	if other, err := OpenPostgres(t.Context(), url); !errors.Is(err, ErrLocked) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("second writer: %v", err)
	}
	otherURL := pgtest.URL(t)
	testPostgres(t, otherURL)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	testPostgres(t, url)
}

func TestPostgresSchemaVersion(t *testing.T) {
	url := pgtest.URL(t)
	first := testPostgres(t, url)
	admin := adminConnection(t, url)
	var version int
	if err := admin.QueryRow(t.Context(), `SELECT MAX(version) FROM metronome_schema`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("version %d: %v", version, err)
	}
	first.Close()
	if _, err := admin.Exec(t.Context(), `INSERT INTO metronome_schema(version) VALUES (99)`); err != nil {
		t.Fatal("cannot set test schema version")
	}
	if other, err := OpenPostgres(t.Context(), url); err == nil || !strings.Contains(err.Error(), "newer") {
		if other != nil {
			other.Close()
		}
		t.Fatalf("newer schema: %v", err)
	}
	url = pgtest.URL(t)
	admin = adminConnection(t, url)
	if _, err := admin.Exec(t.Context(), `CREATE TABLE events (id text)`); err != nil {
		t.Fatal("cannot create foreign event table")
	}
	if other, err := OpenPostgres(t.Context(), url); err == nil || !strings.Contains(err.Error(), "did not create") {
		if other != nil {
			other.Close()
		}
		t.Fatalf("foreign event table: %v", err)
	}
	var untouched bool
	if err := admin.QueryRow(t.Context(), `SELECT to_regclass('metronome_schema') IS NULL`).Scan(&untouched); err != nil || !untouched {
		t.Fatal("refusing a foreign event table changed the schema")
	}
}

func TestPostgresFenceOnSchemaVersionChange(t *testing.T) {
	for _, change := range []struct {
		name, modify, restore string
	}{
		{"missing-table", `ALTER TABLE metronome_schema RENAME TO old_version`, `ALTER TABLE old_version RENAME TO metronome_schema`},
		{"missing-column", `ALTER TABLE metronome_schema RENAME COLUMN version TO old_version`, `ALTER TABLE metronome_schema RENAME COLUMN old_version TO version`},
		{"missing-version", `DELETE FROM metronome_schema`, `INSERT INTO metronome_schema(version) VALUES (1)`},
		{"newer-version", `INSERT INTO metronome_schema(version) VALUES (99)`, `DELETE FROM metronome_schema WHERE version=99`},
	} {
		t.Run(change.name, func(t *testing.T) {
			url := pgtest.URL(t)
			s := testPostgres(t, url)
			admin := adminConnection(t, url)
			terminateWriter(t, admin, s)
			if err := closePostgres(s.conn); err != nil {
				t.Fatal("cannot close test writer connection")
			}
			if _, err := admin.Exec(t.Context(), change.modify); err != nil {
				t.Fatal("cannot change test schema version")
			}
			if err := s.Ping(t.Context()); !errors.Is(err, ErrFenced) {
				t.Fatalf("ping after schema change: %v", err)
			}
			if _, err := admin.Exec(t.Context(), change.restore); err != nil {
				t.Fatal("cannot restore test schema version")
			}
			if err := s.Ping(t.Context()); !errors.Is(err, ErrFenced) {
				t.Fatalf("ping after restoring schema: %v", err)
			}
			if _, err := s.Load(t.Context()); !errors.Is(err, ErrFenced) {
				t.Fatalf("load after restoring schema: %v", err)
			}
			if _, err := s.Append(t.Context(), []flow.Event{sampleEvent("one")}); !errors.Is(err, ErrFenced) {
				t.Fatalf("append after restoring schema: %v", err)
			}
		})
	}
}

func sampleEvent(id string) flow.Event {
	return flow.Event{ID: id, Type: "example", At: time.Date(2026, 11, 28, 12, 0, 0, 123, time.UTC), Actor: "pat", Data: []byte(`{}`)}
}

func terminateWriter(t *testing.T, admin *pgx.Conn, s *Postgres) {
	t.Helper()
	var terminated bool
	if err := admin.QueryRow(t.Context(), `SELECT pg_terminate_backend($1, 5000)`, s.conn.PgConn().PID()).Scan(&terminated); err != nil || !terminated {
		t.Fatal("cannot terminate test writer")
	}
}

func TestPostgresReconnectAndFence(t *testing.T) {
	url := pgtest.URL(t)
	s := testPostgres(t, url)
	admin := adminConnection(t, url)
	if _, err := s.Append(t.Context(), []flow.Event{sampleEvent("one")}); err != nil {
		t.Fatal(err)
	}
	terminateWriter(t, admin, s)
	out, err := s.Append(t.Context(), []flow.Event{sampleEvent("two")})
	if err != nil || len(out) != 1 || out[0].Seq != 2 {
		t.Fatalf("reconnected append: %v, %v", out, err)
	}
	terminateWriter(t, admin, s)
	other := testPostgres(t, url)
	if _, err := other.Append(t.Context(), []flow.Event{sampleEvent("three")}); err != nil {
		t.Fatal(err)
	}
	other.Close()
	if _, err := s.Append(t.Context(), []flow.Event{sampleEvent("four")}); !errors.Is(err, ErrFenced) {
		t.Fatalf("append after log moved: %v", err)
	}
	if err := s.Ping(t.Context()); !errors.Is(err, ErrFenced) {
		t.Fatalf("ping after fencing: %v", err)
	}
}

func TestPostgresFenceWhenWriterLockTaken(t *testing.T) {
	url := pgtest.URL(t)
	s := testPostgres(t, url)
	admin := adminConnection(t, url)
	terminateWriter(t, admin, s)
	other := testPostgres(t, url)
	if err := s.Ping(t.Context()); !errors.Is(err, ErrFenced) {
		t.Fatalf("ping while lock taken: %v", err)
	}
	other.Close()
	if _, err := s.Append(t.Context(), []flow.Event{sampleEvent("one")}); !errors.Is(err, ErrFenced) {
		t.Fatalf("append after fence: %v", err)
	}
}

func TestPostgresCancelledAppend(t *testing.T) {
	s := testPostgres(t, pgtest.URL(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := s.Append(ctx, []flow.Event{sampleEvent("one")})
	if err != nil || len(out) != 1 || out[0].Seq != 1 {
		t.Fatalf("cancelled append: %v, %v", out, err)
	}
	out, err = s.Append(t.Context(), []flow.Event{sampleEvent("two")})
	if err != nil || len(out) != 1 || out[0].Seq != 2 {
		t.Fatalf("next append: %v, %v", out, err)
	}
}

func TestPostgresConnectionErrorsRedactURL(t *testing.T) {
	for _, url := range []string{
		"postgres://pat:s3cret-example@127.0.0.1:1/metronome?application_name=acme&connect_timeout=1",
		"postgres://pat:s3cret-example@%invalid/metronome",
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		s, err := OpenPostgres(ctx, url)
		cancel()
		if s != nil {
			s.Close()
		}
		if err == nil {
			t.Fatal("unexpected connection success")
		}
		for _, secret := range []string{url, "s3cret-example", "pat:", "application_name", "connect_timeout"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatal("connection error disclosed URL contents")
			}
		}
	}
}
