package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

type eventStore interface {
	flow.Store
	api.Pinger
	Close() error
}

func isPostgres(db string) bool {
	return strings.HasPrefix(db, "postgres://") || strings.HasPrefix(db, "postgresql://")
}

var connectionKeyword = regexp.MustCompile(`(?i)(^|[^[:alnum:]_])(host|hostaddr|password|user|dbname|port|sslmode|sslpassword|service|passfile)\s*=`)

func validateSQLitePath(path string) error {
	trimmed := strings.ToLower(strings.TrimSpace(path))
	if strings.Contains(path, "://") || strings.HasPrefix(trimmed, "postgres:") || strings.HasPrefix(trimmed, "postgresql:") || connectionKeyword.MatchString(path) {
		return usagef("expected an SQLite file path, not a connection string; PostgreSQL requires a postgres:// or postgresql:// URL")
	}
	return nil
}

func resolveDB(fs *flag.FlagSet, directFlag, direct, envFlag, env string) (string, error) {
	var directSet, envSet bool
	fs.Visit(func(option *flag.Flag) {
		if option.Name == directFlag {
			directSet = true
		}
		if option.Name == envFlag {
			envSet = true
		}
	})
	if !envSet {
		return direct, nil
	}
	if directSet {
		return "", usagef("--%s and --%s are mutually exclusive", directFlag, envFlag)
	}
	value := os.Getenv(env)
	if value == "" {
		return "", usagef("environment variable %q is unset or empty", env)
	}
	return value, nil
}

func openEventStore(ctx context.Context, db string) (eventStore, string, error) {
	if isPostgres(db) {
		st, err := store.OpenPostgres(ctx, db)
		if err != nil {
			return nil, "", err
		}
		return st, st.Description(), nil
	}
	if err := validateSQLitePath(db); err != nil {
		return nil, "", err
	}
	st, err := store.OpenSQLite(ctx, db)
	return st, db, err
}

func (a *app) migrateStore(ctx context.Context, args []string) error {
	fs := newFlags("migrate-store")
	from := fs.String("from", "", "")
	to := fs.String("to", "", "")
	toEnv := fs.String("to-env", "", "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if *from == "" {
		return usagef("--from FILE is required")
	}
	if err := validateSQLitePath(*from); err != nil {
		return usagef("--from must be an SQLite file path, not a connection string")
	}
	db, err := resolveDB(fs, "to", *to, "to-env", *toEnv)
	if err != nil {
		return err
	}
	if !isPostgres(db) {
		return usagef("--to URL or --to-env NAME must select PostgreSQL")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	dst, err := store.OpenPostgres(ctx, db)
	if err != nil {
		return err
	}
	defer dst.Close()
	result, err := store.CopySQLite(ctx, *from, dst)
	if err != nil {
		return err
	}
	if result.Copied == 0 {
		fmt.Fprintf(a.out, "%s already held all %d events from %s; replayed state matches\n", dst.Description(), result.Events, *from)
	} else {
		fmt.Fprintf(a.out, "copied %d events (last seq %d) from %s to %s; replayed state matches\n", result.Copied, result.LastSeq, *from, dst.Description())
	}
	return nil
}
