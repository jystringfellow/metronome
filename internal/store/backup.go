package store

import (
	"context"
	"database/sql"
	"fmt"
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
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return BackupResult{}, fmt.Errorf("no database at %s", src)
	} else if err != nil {
		return BackupResult{}, err
	}
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return BackupResult{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return BackupResult{}, err
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='events')`).Scan(&exists); err != nil {
		return BackupResult{}, err
	}
	if !exists {
		return BackupResult{}, fmt.Errorf("%s has no metronome event log", src)
	}
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
