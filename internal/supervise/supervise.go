package supervise

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

type Loop struct {
	Name  string
	Every time.Duration
	Round func(context.Context) error
}

type Options struct {
	Wait     func(context.Context, time.Duration) error
	Now      func() time.Time
	Out, Err io.Writer
}

func Run(ctx context.Context, loops []Loop, options Options) {
	if options.Wait == nil {
		options.Wait = wait
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.Err == nil {
		options.Err = io.Discard
	}
	var group sync.WaitGroup
	var mutex sync.Mutex
	for _, loop := range loops {
		group.Go(func() { run(ctx, loop, options, &mutex) })
	}
	group.Wait()
}

func run(ctx context.Context, loop Loop, options Options, mutex *sync.Mutex) {
	var backoff time.Duration
	for round := 1; ctx.Err() == nil; round++ {
		start := options.Now()
		err := invoke(ctx, loop.Round)
		elapsed := options.Now().Sub(start)
		if ctx.Err() != nil {
			return
		}
		delay := max(0, loop.Every-elapsed)
		mutex.Lock()
		if err == nil {
			backoff = 0
			fmt.Fprintf(options.Out, "%s: round %d ok in %s\n", loop.Name, round, elapsed)
		} else {
			if backoff == 0 {
				backoff = min(time.Second, loop.Every)
			} else if backoff >= loop.Every-backoff {
				backoff = loop.Every
			} else {
				backoff *= 2
			}
			delay = backoff
			fmt.Fprintf(options.Err, "%s: round %d failed in %s: %v; retrying in %s\n", loop.Name, round, elapsed, err, delay)
		}
		mutex.Unlock()
		if options.Wait(ctx, delay) != nil {
			return
		}
	}
}

func invoke(ctx context.Context, round func(context.Context) error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("panic: %v", value)
		}
	}()
	return round(ctx)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
