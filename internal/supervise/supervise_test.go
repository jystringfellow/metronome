package supervise

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSuperviseBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out, errOut bytes.Buffer
	var delays []time.Duration
	now := time.Unix(0, 0)
	round := 0
	Run(ctx, []Loop{{Name: "intake example", Every: 5 * time.Second, Round: func(context.Context) error {
		round++
		now = now.Add(time.Second)
		if round == 1 {
			panic("example panic")
		}
		if round == 5 {
			return nil
		}
		return errors.New("example failure")
	}}}, Options{Out: &out, Err: &errOut, Now: func() time.Time { return now }, Wait: func(ctx context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		if len(delays) == 6 {
			cancel()
		}
		return ctx.Err()
	}})
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 4 * time.Second, time.Second}
	if !slices.Equal(delays, want) {
		t.Fatal(delays)
	}
	if !strings.Contains(out.String(), "round 5 ok in 1s") || !strings.Contains(errOut.String(), "round 1 failed in 1s: panic: example panic; retrying in 1s") || strings.Count(errOut.String(), "failed") != 5 {
		t.Fatal(out.String(), errOut.String())
	}
}

func TestSuperviseIndependentCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	failed := make(chan struct{})
	healthy := make(chan struct{})
	var out, errOut bytes.Buffer
	Run(ctx, []Loop{
		{Name: "bad", Every: time.Hour, Round: func(ctx context.Context) error { close(failed); return errors.New("failure") }},
		{Name: "good", Every: time.Hour, Round: func(ctx context.Context) error { <-failed; close(healthy); <-ctx.Done(); return ctx.Err() }},
	}, Options{Out: &out, Err: &errOut, Wait: func(ctx context.Context, _ time.Duration) error { <-healthy; cancel(); return ctx.Err() }})
	if !strings.Contains(errOut.String(), "bad: round 1 failed") || strings.Contains(errOut.String(), "good:") {
		t.Fatal(errOut.String())
	}
}
