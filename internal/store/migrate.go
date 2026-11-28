package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jystringfellow/metronome/internal/flow"
)

type CopyResult struct {
	Copied  int64
	Events  int64
	LastSeq int64
}

type replayLog []flow.Event

func (log replayLog) Load(context.Context) ([]flow.Event, error) { return log, nil }

func (log replayLog) Append(context.Context, []flow.Event) ([]flow.Event, error) {
	return nil, errors.New("read-only replay log")
}

func sameEvent(left, right flow.Event) bool {
	return left.Seq == right.Seq && left.ID == right.ID && left.Type == right.Type && left.At.Equal(right.At) && left.Actor == right.Actor && bytes.Equal(left.Data, right.Data)
}

func compareReplay(ctx context.Context, source, target []flow.Event) error {
	left, err := flow.NewEngine(ctx, flow.DefaultConfig(), replayLog(source), nil)
	if err != nil {
		return fmt.Errorf("replay source: %w", err)
	}
	right, err := flow.NewEngine(ctx, flow.DefaultConfig(), replayLog(target), nil)
	if err != nil {
		return fmt.Errorf("replay destination: %w", err)
	}
	var equal bool
	left.View(func(state *flow.State) {
		right.View(func(other *flow.State) { equal = reflect.DeepEqual(state, other) })
	})
	if !equal {
		return errors.New("replayed state differs")
	}
	return nil
}

func readSQLiteLog(ctx context.Context, src string) (*SQLite, []flow.Event, error) {
	source, err := openExistingSQLite(ctx, src)
	if err != nil {
		return nil, nil, err
	}
	events, err := source.Load(ctx)
	if err == nil {
		for index, ev := range events {
			if ev.Seq != int64(index)+1 {
				err = fmt.Errorf("source event log has a gap at sequence %d", index+1)
				break
			}
		}
	}
	if err != nil {
		source.Close()
		return nil, nil, err
	}
	return source, events, nil
}

func CopySQLite(ctx context.Context, src string, dst *Postgres) (CopyResult, error) {
	source, events, err := readSQLiteLog(ctx, src)
	if err != nil {
		return CopyResult{}, err
	}
	defer source.Close()
	if _, err := flow.NewEngine(ctx, flow.DefaultConfig(), replayLog(events), nil); err != nil {
		return CopyResult{}, fmt.Errorf("replay source: %w", err)
	}
	loaded, err := dst.Load(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	prefix := len(loaded) <= len(events)
	if prefix {
		for index, ev := range loaded {
			if !sameEvent(ev, events[index]) {
				prefix = false
				break
			}
		}
	}
	if !prefix {
		return CopyResult{}, fmt.Errorf("destination already holds %d events that do not match %s; use an empty database", len(loaded), src)
	}
	dst.mu.Lock()
	if err = dst.checkSequence(ctx); err == nil {
		if dst.lastSeq != int64(len(loaded)) {
			err = errors.New("destination changed during copy")
		} else {
			_, err = dst.append(ctx, events[len(loaded):])
		}
	}
	dst.mu.Unlock()
	if err != nil {
		return CopyResult{}, err
	}
	target, err := dst.Load(ctx)
	if err != nil {
		return CopyResult{}, err
	}
	if len(target) != len(events) {
		return CopyResult{}, errors.New("destination event count differs")
	}
	for index, ev := range target {
		if !sameEvent(ev, events[index]) {
			return CopyResult{}, fmt.Errorf("destination event differs at sequence %d", index+1)
		}
	}
	if err := compareReplay(ctx, events, target); err != nil {
		return CopyResult{}, err
	}
	var lastSeq int64
	if err := source.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events`).Scan(&lastSeq); err != nil {
		return CopyResult{}, err
	}
	if lastSeq != int64(len(events)) {
		return CopyResult{}, fmt.Errorf("%s gained events during the copy; stop every writer on %s and run migrate-store again", src, src)
	}
	return CopyResult{Copied: int64(len(events) - len(loaded)), Events: int64(len(events)), LastSeq: lastSeq}, nil
}
