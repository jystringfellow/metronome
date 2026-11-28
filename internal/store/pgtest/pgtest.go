package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func URL(t testing.TB) string {
	t.Helper()
	raw := os.Getenv("METRONOME_TEST_POSTGRES_URL")
	if raw == "" {
		t.Skip("METRONOME_TEST_POSTGRES_URL is not set; skipping the PostgreSQL run")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("invalid PostgreSQL test URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal("cannot connect to PostgreSQL test database")
	}
	defer conn.Close(context.Background())
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "metronome_test_" + hex.EncodeToString(random)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("cannot create PostgreSQL test schema")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanup, err := pgx.Connect(ctx, raw)
		if err != nil {
			t.Error("cannot reconnect to clean up PostgreSQL test schema")
			return
		}
		defer cleanup.Close(ctx)
		if _, err := cleanup.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("cannot drop PostgreSQL test schema")
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
