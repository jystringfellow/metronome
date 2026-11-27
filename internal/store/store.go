package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
	_ "modernc.org/sqlite"
)

var ErrDuplicateID = errors.New("duplicate event id")

type Memory struct {
	mu     sync.Mutex
	events []flow.Event
	ids    map[string]bool
}

func NewMemory() *Memory { return &Memory{ids: map[string]bool{}} }

func (m *Memory) Load(context.Context) ([]flow.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]flow.Event(nil), m.events...), nil
}

func (m *Memory) Append(_ context.Context, events []flow.Event) ([]flow.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ev := range events {
		if m.ids[ev.ID] {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateID, ev.ID)
		}
	}
	out := make([]flow.Event, len(events))
	for i, ev := range events {
		ev.Seq = int64(len(m.events) + 1)
		m.ids[ev.ID] = true
		m.events = append(m.events, ev)
		out[i] = ev
	}
	return out, nil
}

type SQLite struct {
	db *sql.DB
}

func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS events (
			seq   INTEGER PRIMARY KEY AUTOINCREMENT,
			id    TEXT NOT NULL UNIQUE,
			type  TEXT NOT NULL,
			at    TEXT NOT NULL,
			actor TEXT NOT NULL,
			data  BLOB NOT NULL
		)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init %s: %w", path, err)
		}
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Load(ctx context.Context) ([]flow.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, id, type, at, actor, data FROM events ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []flow.Event
	for rows.Next() {
		var ev flow.Event
		var at string
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.Type, &at, &ev.Actor, &ev.Data); err != nil {
			return nil, err
		}
		if ev.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("event %s: %w", ev.ID, err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *SQLite) Append(ctx context.Context, events []flow.Event) ([]flow.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := make([]flow.Event, len(events))
	for i, ev := range events {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE id = ?)`, ev.ID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateID, ev.ID)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO events (id, type, at, actor, data) VALUES (?, ?, ?, ?, ?)`,
			ev.ID, ev.Type, ev.At.UTC().Format(time.RFC3339Nano), ev.Actor, []byte(ev.Data))
		if err != nil {
			return nil, err
		}
		if ev.Seq, err = res.LastInsertId(); err != nil {
			return nil, err
		}
		ev.At = ev.At.UTC()
		out[i] = ev
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
