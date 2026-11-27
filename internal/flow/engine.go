package flow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Store interface {
	Load(ctx context.Context) ([]Event, error)
	Append(ctx context.Context, events []Event) ([]Event, error)
}

type ErrorCode string

const (
	CodeInvalid    ErrorCode = "invalid"
	CodeNotFound   ErrorCode = "not_found"
	CodeConflict   ErrorCode = "conflict"
	CodeNoCapacity ErrorCode = "no_capacity"
	CodeNoWork     ErrorCode = "no_work"
)

type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func fail(code ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func errUnknown(kind, id string) error {
	return fail(CodeNotFound, "unknown %s %q", kind, id)
}

func CodeOf(err error) ErrorCode {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return ""
}

type Result struct {
	Events    []Event `json:"events"`
	Duplicate bool    `json:"duplicate,omitempty"`
}

type Engine struct {
	mu     sync.Mutex
	cfg    Config
	store  Store
	clock  func() time.Time
	state  *State
	events []Event
	byID   map[string]int
}

func NewEngine(ctx context.Context, cfg Config, store Store, clock func() time.Time) (*Engine, error) {
	if clock == nil {
		clock = time.Now
	}
	e := &Engine{cfg: cfg, store: store, clock: clock, state: NewState(), byID: map[string]int{}}
	events, err := store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	for _, ev := range events {
		if err := e.state.Apply(ev); err != nil {
			return nil, err
		}
		e.record(ev)
	}
	return e, nil
}

func (e *Engine) Config() Config { return e.cfg }

func (e *Engine) record(ev Event) {
	e.byID[ev.ID] = len(e.events)
	e.events = append(e.events, ev)
}

func (e *Engine) View(fn func(s *State)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e.state)
}

func (e *Engine) EventsAfter(seq int64, limit int) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Event
	for _, ev := range e.events {
		if ev.Seq > seq {
			out = append(out, ev)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

func (e *Engine) Tick(ctx context.Context) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tickLocked(ctx, e.clock())
}

func (e *Engine) tickLocked(ctx context.Context, now time.Time) (Result, error) {
	d := e.newDecider(now, "metronome", "")
	d.tick()
	return e.commit(ctx, d)
}

func (e *Engine) execute(ctx context.Context, actor, requestID string, fn func(d *decider) error) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if requestID != "" {
		if i, ok := e.byID[eventID(requestID, 0)]; ok {
			var prior []Event
			for j := i; j < len(e.events) && isOfRequest(e.events[j].ID, requestID); j++ {
				prior = append(prior, e.events[j])
			}
			return Result{Events: prior, Duplicate: true}, nil
		}
	}
	now := e.clock()
	if _, err := e.tickLocked(ctx, now); err != nil {
		return Result{}, err
	}
	d := e.newDecider(now, actor, requestID)
	if err := fn(d); err != nil {
		return Result{}, err
	}
	return e.commit(ctx, d)
}

func (e *Engine) commit(ctx context.Context, d *decider) (Result, error) {
	if len(d.events) == 0 {
		return Result{Events: []Event{}}, nil
	}
	stored, err := e.store.Append(ctx, d.events)
	if err != nil {
		return Result{}, fmt.Errorf("append events: %w", err)
	}
	for _, ev := range stored {
		if err := e.state.Apply(ev); err != nil {
			return Result{}, err
		}
		e.record(ev)
	}
	return Result{Events: stored}, nil
}

func (e *Engine) newDecider(now time.Time, actor, requestID string) *decider {
	return &decider{s: e.state, cfg: e.cfg, now: now, actor: actor, requestID: requestID}
}

func eventID(requestID string, i int) string {
	return fmt.Sprintf("%s/%d", requestID, i)
}

func isOfRequest(id, requestID string) bool {
	return len(id) > len(requestID) && id[:len(requestID)+1] == requestID+"/"
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

type decider struct {
	s         *State
	cfg       Config
	now       time.Time
	actor     string
	requestID string
	events    []Event
	newItems  int
	newRuns   int
	newFlags  int

	closedFlags map[string]bool
}

func (d *decider) emit(typ string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	id := randomID()
	if d.requestID != "" {
		id = eventID(d.requestID, len(d.events))
	}
	d.events = append(d.events, Event{ID: id, Type: typ, At: d.now, Actor: d.actor, Data: data})
}

func (d *decider) nextItemID() string {
	d.newItems++
	return fmt.Sprintf("I%d", len(d.s.Items)+d.newItems)
}

func (d *decider) nextRunID() string {
	d.newRuns++
	return fmt.Sprintf("R%d", len(d.s.Runs)+d.newRuns)
}

func (d *decider) nextFlagID() string {
	d.newFlags++
	return fmt.Sprintf("F%d", len(d.s.Flags)+d.newFlags)
}
