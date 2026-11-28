package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

type BackupResult struct {
	Events  int64
	LastSeq int64
}

func Backup(ctx context.Context, src, dest string) (BackupResult, error) {
	if _, err := os.Lstat(dest); err == nil {
		return BackupResult{}, fmt.Errorf("%s already exists", dest)
	} else if !os.IsNotExist(err) {
		return BackupResult{}, err
	}
	source, err := openExistingSQLite(ctx, src)
	if err != nil {
		return BackupResult{}, err
	}
	defer source.Close()
	db := source.db
	dir := filepath.Dir(dest)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".*.tmp")
	if err != nil {
		return BackupResult{}, err
	}
	defer os.Remove(temp.Name())
	if err := temp.Close(); err != nil {
		return BackupResult{}, err
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, temp.Name()); err != nil {
		return BackupResult{}, err
	}
	result, err := verifyBackup(ctx, temp.Name())
	if err != nil {
		return BackupResult{}, err
	}
	file, err := os.OpenFile(temp.Name(), os.O_RDWR, 0)
	if err != nil {
		return BackupResult{}, err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return BackupResult{}, syncErr
	}
	if closeErr != nil {
		return BackupResult{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		return BackupResult{}, err
	}
	if err := os.Link(temp.Name(), dest); os.IsExist(err) {
		return BackupResult{}, fmt.Errorf("%s already exists", dest)
	} else if err != nil {
		return BackupResult{}, err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return result, nil
}

func openExistingSQLite(ctx context.Context, src string) (*SQLite, error) {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil, fmt.Errorf("no database at %s", src)
	} else if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='events')`).Scan(&exists); err != nil {
		db.Close()
		return nil, err
	}
	if !exists {
		db.Close()
		return nil, fmt.Errorf("%s has no metronome event log", src)
	}
	return &SQLite{db: db}, nil
}

func verifyBackup(ctx context.Context, path string) (BackupResult, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return BackupResult{}, err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return BackupResult{}, err
	}
	if integrity != "ok" {
		return BackupResult{}, fmt.Errorf("backup integrity check: %s", integrity)
	}
	var result BackupResult
	err = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(seq),0) FROM events`).Scan(&result.Events, &result.LastSeq)
	return result, err
}
