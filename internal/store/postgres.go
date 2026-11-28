package store

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jystringfellow/metronome/internal/flow"
)

var (
	ErrInvalidPostgresURL = errors.New("invalid PostgreSQL connection string")
	ErrLocked             = errors.New("another metronome instance holds the writer lock; only one writer runs")
	ErrFenced             = errors.New("metronome lost its PostgreSQL writer lock and the event log may have changed; restart to replay")
)

const postgresVersion = 1

type Postgres struct {
	mu             sync.Mutex
	config         *pgx.ConnConfig
	conn           *pgx.Conn
	schema         string
	lastSeq        int64
	fenced, closed bool
}

func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, ErrInvalidPostgresURL
	}
	config.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, DeadlineDelay: time.Second}
	}
	s := &Postgres{config: config}
	if err := s.connect(ctx, true); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Postgres) Description() string {
	return fmt.Sprintf("PostgreSQL database %q on %q:%d", s.config.Database, s.config.Host, s.config.Port)
}

func postgresError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("PostgreSQL %s: %w", operation, contextError(err))
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("PostgreSQL %s failed (SQLSTATE %s)", operation, pgErr.Code)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("PostgreSQL %s failed: connection refused", operation)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("PostgreSQL %s failed: hostname lookup failed", operation)
	}
	return fmt.Errorf("PostgreSQL %s failed", operation)
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return context.DeadlineExceeded
}

func (s *Postgres) connect(ctx context.Context, initial bool) error {
	conn, err := pgx.ConnectConfig(ctx, s.config.Copy())
	if err != nil {
		return fmt.Errorf("connect to %s: %w", s.Description(), postgresError("connection", err))
	}
	keep := false
	defer func() {
		if !keep {
			_ = closePostgres(conn)
		}
	}()
	var schema *string
	if err := conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return postgresError("read schema", err)
	}
	if schema == nil {
		if !initial {
			s.fenced = true
			return ErrFenced
		}
		return errors.New("PostgreSQL search_path has no current schema")
	}
	if !initial && *schema != s.schema {
		s.fenced = true
		return ErrFenced
	}
	key := fnv.New64a()
	_, _ = key.Write([]byte("metronome:" + *schema))
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(key.Sum64())).Scan(&locked); err != nil {
		return postgresError("acquire writer lock", err)
	}
	if !locked {
		if !initial {
			s.fenced = true
			return ErrFenced
		}
		return fmt.Errorf("%w (%s, schema %q)", ErrLocked, s.Description(), *schema)
	}
	if _, err := conn.Exec(ctx, `SET search_path TO `+pgx.Identifier{*schema}.Sanitize()); err != nil {
		return postgresError("set schema", err)
	}
	if err := setupPostgres(ctx, conn, initial); err != nil {
		if errors.Is(err, ErrFenced) {
			s.fenced = true
		}
		return err
	}
	var seq int64
	if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq); err != nil {
		return postgresError("read sequence", err)
	}
	if !initial && seq != s.lastSeq {
		s.fenced = true
		return ErrFenced
	}
	s.conn, s.schema, s.lastSeq = conn, *schema, seq
	keep = true
	return nil
}

func setupPostgres(ctx context.Context, conn *pgx.Conn, initial bool) error {
	hasVersionTable, err := postgresRelationExists(ctx, conn, "metronome_schema")
	if err != nil {
		return err
	}
	var version int
	if hasVersionTable {
		if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM metronome_schema`).Scan(&version); err != nil {
			var pgErr *pgconn.PgError
			if !initial && errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42703") {
				return ErrFenced
			}
			return postgresError("read schema version", err)
		}
	}
	if !initial && version != postgresVersion {
		return ErrFenced
	}
	if version > postgresVersion {
		return fmt.Errorf("schema version %d is newer than this metronome supports (%d); upgrade metronome", version, postgresVersion)
	}
	if version == postgresVersion {
		return nil
	}
	exists, err := postgresRelationExists(ctx, conn, "events")
	if err != nil {
		return err
	}
	if exists {
		return errors.New("PostgreSQL schema has an events table metronome did not create")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return postgresError("begin schema migration", err)
	}
	defer rollbackPostgres(tx)
	if !hasVersionTable {
		if _, err := tx.Exec(ctx, `CREATE TABLE metronome_schema (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return postgresError("create schema version table", err)
		}
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE events (seq bigint PRIMARY KEY CHECK (seq > 0), id text NOT NULL UNIQUE, type text NOT NULL, at text NOT NULL, actor text NOT NULL, data bytea NOT NULL)`); err != nil {
		return postgresError("create event table", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO metronome_schema(version) VALUES (1)`); err != nil {
		return postgresError("record schema version", err)
	}
	return postgresError("commit schema migration", tx.Commit(ctx))
}

func postgresRelationExists(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1)`, name).Scan(&exists)
	return exists, postgresError("check schema relation", err)
}

func (s *Postgres) ensure(ctx context.Context) error {
	if s.fenced {
		return ErrFenced
	}
	if s.closed {
		return errors.New("PostgreSQL store is closed")
	}
	if s.conn.IsClosed() {
		return s.connect(ctx, false)
	}
	return nil
}

func (s *Postgres) checkSequence(ctx context.Context) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	var seq int64
	err := s.conn.QueryRow(ctx, `SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq)
	if err != nil && s.conn.IsClosed() && ctx.Err() == nil {
		return s.connect(ctx, false)
	}
	if err != nil {
		return postgresError("read sequence", err)
	}
	if seq != s.lastSeq {
		s.fenced = true
		return ErrFenced
	}
	return nil
}

func (s *Postgres) Ping(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkSequence(ctx)
}

func (s *Postgres) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return postgresError("close", closePostgres(s.conn))
}

func closePostgres(conn *pgx.Conn) error {
	if conn == nil || conn.IsClosed() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock_all()`)
	return conn.Close(ctx)
}

func rollbackPostgres(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Postgres) Load(ctx context.Context) ([]flow.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkSequence(ctx); err != nil {
		return nil, err
	}
	rows, err := s.conn.Query(ctx, `SELECT seq,id,type,at,actor,data FROM events ORDER BY seq`)
	if err != nil {
		return nil, postgresError("load events", err)
	}
	defer rows.Close()
	var out []flow.Event
	for rows.Next() {
		var ev flow.Event
		var at string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Type, &at, &ev.Actor, &ev.Data); err != nil {
			return nil, postgresError("read event", err)
		}
		if ev.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, errors.New("invalid PostgreSQL event timestamp")
		}
		out = append(out, ev)
	}
	return out, postgresError("load events", rows.Err())
}

func (s *Postgres) Append(ctx context.Context, events []flow.Event) ([]flow.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.checkSequence(ctx); err != nil {
		return nil, err
	}
	return s.append(ctx, events)
}

func (s *Postgres) append(ctx context.Context, events []flow.Event) ([]flow.Event, error) {
	ids := make([]string, 0, len(events))
	seen := make(map[string]bool, len(events))
	for _, ev := range events {
		if seen[ev.ID] {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateID, ev.ID)
		}
		seen[ev.ID] = true
		ids = append(ids, ev.ID)
	}
	out := make([]flow.Event, len(events))
	if len(events) == 0 {
		return out, nil
	}
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return nil, postgresError("begin append", err)
	}
	defer rollbackPostgres(tx)
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&seq); err != nil {
		return nil, postgresError("read sequence", err)
	}
	if seq != s.lastSeq {
		s.fenced = true
		return nil, ErrFenced
	}
	var duplicate string
	err = tx.QueryRow(ctx, `SELECT id FROM events WHERE id = ANY($1) LIMIT 1`, ids).Scan(&duplicate)
	if err == nil {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateID, duplicate)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, postgresError("check duplicate IDs", err)
	}
	for index, ev := range events {
		ev.Seq = seq + int64(index) + 1
		ev.At = ev.At.UTC()
		out[index] = ev
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"events"}, []string{"seq", "id", "type", "at", "actor", "data"}, pgx.CopyFromSlice(len(out), func(index int) ([]any, error) {
		ev := out[index]
		return []any{ev.Seq, ev.ID, ev.Type, ev.At.Format(time.RFC3339Nano), ev.Actor, []byte(ev.Data)}, nil
	}))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "events_id_key" {
			return nil, ErrDuplicateID
		}
		return nil, postgresError("append events", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, postgresError("commit append", err)
	}
	s.lastSeq = out[len(out)-1].Seq
	return out, nil
}
