package cli_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/store"
	"github.com/jystringfellow/metronome/internal/store/pgtest"
)

func TestDatabaseFlagsAndRedaction(t *testing.T) {
	t.Chdir(t.TempDir())
	const dsn = "postgres://pat:s3cret-example@127.0.0.1:1/metronome?connect_timeout=1"
	t.Setenv("METRONOME_TEST_DB", dsn)
	t.Setenv("UNSET_EXAMPLE", "")
	for _, test := range []struct {
		args    []string
		code    int
		message string
	}{
		{[]string{"serve", "--db-env", "UNSET_EXAMPLE"}, 2, "UNSET_EXAMPLE"},
		{[]string{"serve", "--db", "example.db", "--db-env", "METRONOME_TEST_DB"}, 2, "mutually exclusive"},
		{[]string{"serve", "--db", dsn}, 1, "PostgreSQL"},
		{[]string{"serve", "--db-env", "METRONOME_TEST_DB"}, 1, "PostgreSQL"},
		{[]string{"backup", "--db", dsn, "--to", "example.db"}, 2, "pg_dump"},
		{[]string{"backup", "--db-env", "METRONOME_TEST_DB", "--to", "example.db"}, 2, "pg_dump"},
		{[]string{"migrate-store", "--to", dsn}, 2, "--from"},
		{[]string{"migrate-store", "--from", dsn, "--to", dsn}, 2, "--from"},
		{[]string{"migrate-store", "--from", "example.db"}, 2, "--to"},
		{[]string{"migrate-store", "--from", "example.db", "--to", "target.db"}, 2, "PostgreSQL"},
		{[]string{"migrate-store", "--from", "example.db", "--to", dsn, "--to-env", "METRONOME_TEST_DB"}, 2, "mutually exclusive"},
		{[]string{"migrate-store", "--from", "example.db", "--to-env", "METRONOME_TEST_DB"}, 1, "PostgreSQL"},
	} {
		var out, errOut bytes.Buffer
		code := cli.Run(t.Context(), test.args, &out, &errOut)
		logs := out.String() + errOut.String()
		if strings.Contains(logs, dsn) || strings.Contains(logs, "s3cret-example") {
			t.Fatal("database credentials disclosed")
		}
		if code != test.code || !strings.Contains(logs, test.message) {
			t.Fatalf("%s: exit %d, want %d with %q: %s", test.args[0], code, test.code, test.message, logs)
		}
	}
	for _, invalid := range []struct {
		name, value string
	}{
		{"keyword-dsn", "host=db.example.com user=pat password=s3cret-example dbname=metronome"},
		{"keyword-whitespace", "PASSWORD = 's3cret-example'"},
		{"keyword-user", "user=s3cret-example"},
		{"keyword-database", "dbname=s3cret-example"},
		{"keyword-host", "host=s3cret-example"},
		{"single-slash", "postgres:/pat:s3cret-example@db.example.com/metronome"},
		{"no-slash", "postgresql:pat:s3cret-example@db.example.com/metronome"},
		{"uppercase", "POSTGRES://pat:s3cret-example@db.example.com/metronome"},
		{"mixed-case", "PostgreSQL:/pat:s3cret-example@db.example.com/metronome"},
		{"other-scheme", "mysql://pat:s3cret-example@db.example.com/metronome"},
		{"leading-space", " postgres://pat:s3cret-example@db.example.com/metronome"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			t.Setenv("METRONOME_TEST_DB", invalid.value)
			for _, args := range [][]string{
				{"serve", "--db", invalid.value, "--addr", "127.0.0.1:0"},
				{"serve", "--db-env", "METRONOME_TEST_DB", "--addr", "127.0.0.1:0"},
				{"backup", "--db", invalid.value, "--to", "backup.db"},
				{"backup", "--db-env", "METRONOME_TEST_DB", "--to", "backup.db"},
				{"migrate-store", "--from", invalid.value, "--to", dsn},
			} {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				var out, errOut bytes.Buffer
				code := cli.Run(ctx, args, &out, &errOut)
				cancel()
				logs := out.String() + errOut.String()
				if strings.Contains(logs, invalid.value) || strings.Contains(logs, "s3cret-example") {
					t.Fatal("invalid connection string disclosed")
				}
				if code != 2 || !strings.Contains(logs, "SQLite file path") {
					t.Fatalf("%s: exit %d, want usage error for connection string", args[0], code)
				}
				entries, err := os.ReadDir(".")
				if err != nil || len(entries) != 0 {
					t.Fatal("invalid connection string created a file or directory")
				}
			}
		})
	}
}

func serveFromEnv(t *testing.T, db string, check func(context.Context, *api.Client)) (string, string) {
	t.Helper()
	t.Setenv("METRONOME_TEST_DB", db)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var out, errOut bytes.Buffer
	started := make(chan string, 1)
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"serve", "--db-env", "METRONOME_TEST_DB", "--addr", "127.0.0.1:0", "--tick", "1h"}, serveOutput{&out, started}, &errOut)
	}()
	func() {
		defer func() {
			cancel()
			if code := <-done; code != 0 {
				t.Errorf("serve exited %d", code)
			}
		}()
		select {
		case address := <-started:
			client := &api.Client{BaseURL: address, Actor: "pat"}
			for {
				var readiness map[string]any
				if err := client.Get(ctx, "/readyz", &readiness); err == nil && readiness["status"] == "ready" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("service did not become ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			check(ctx, client)
		case <-ctx.Done():
			t.Fatal("serve did not start")
		}
	}()
	return out.String(), errOut.String()
}

func TestServeSQLiteFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.db")
	out, _ := serveFromEnv(t, path, func(ctx context.Context, client *api.Client) {
		var board api.Board
		if err := client.Get(ctx, "/v1/board", &board); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "with "+path) {
		t.Fatal("SQLite serving description missing")
	}
}

func assertNoPostgresSecrets(t *testing.T, dsn, logs string) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test URL")
	}
	password, _ := parsed.User.Password()
	if strings.Contains(logs, dsn) || (password != "" && strings.Contains(logs, password)) {
		t.Fatal("PostgreSQL connection credentials disclosed")
	}
}

func TestServePostgresAndMigrate(t *testing.T) {
	dsn := pgtest.URL(t)
	path := filepath.Join(t.TempDir(), "example.db")
	s, err := store.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := newEnvWithStore(t, s)
	e.ok("pat", "workstream", "create", "example")
	want := e.board()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRONOME_TEST_DB", dsn)
	var out, errOut bytes.Buffer
	code := cli.Run(t.Context(), []string{"migrate-store", "--from", path, "--to-env", "METRONOME_TEST_DB"}, &out, &errOut)
	assertNoPostgresSecrets(t, dsn, out.String()+errOut.String())
	if code != 0 || !strings.Contains(out.String(), "replayed state matches") {
		t.Fatalf("migration failed (exit %d)", code)
	}
	logs, errors := serveFromEnv(t, dsn, func(ctx context.Context, client *api.Client) {
		var board api.Board
		if err := client.Get(ctx, "/v1/board", &board); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(board, want) {
			t.Fatal("migrated board differs")
		}
		var secondOut, secondErr bytes.Buffer
		code := cli.Run(ctx, []string{"serve", "--db-env", "METRONOME_TEST_DB", "--addr", "127.0.0.1:0"}, &secondOut, &secondErr)
		assertNoPostgresSecrets(t, dsn, secondOut.String()+secondErr.String())
		if code != 1 || !strings.Contains(secondErr.String(), "writer lock") || strings.Contains(secondOut.String(), "serving on") {
			t.Fatalf("second writer exit %d", code)
		}
		secondOut.Reset()
		secondErr.Reset()
		code = cli.Run(ctx, []string{"migrate-store", "--from", path, "--to-env", "METRONOME_TEST_DB"}, &secondOut, &secondErr)
		assertNoPostgresSecrets(t, dsn, secondOut.String()+secondErr.String())
		if code != 1 || !strings.Contains(secondErr.String(), "writer lock") {
			t.Fatalf("migration while writer holds lock: exit %d", code)
		}
	})
	assertNoPostgresSecrets(t, dsn, logs+errors)
	if !strings.Contains(logs, "with PostgreSQL database") {
		t.Fatal("PostgreSQL serving description missing")
	}
}
