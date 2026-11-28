# Running Metronome as a service

Run one `metronome serve` instance with the SQLite database on persistent storage. Keep the unauthenticated
board and API behind an authenticating proxy; probes can reach the service directly.

## Probes

The probes use the service address and return JSON with `Cache-Control: no-store`.

```sh
curl -i http://127.0.0.1:7340/healthz
curl -i http://127.0.0.1:7340/readyz
```

- `/healthz` returns 200 `{"status":"ok"}` without accessing the database. Use it for liveness and startup.
- `/readyz` returns 503 `{"status":"starting"}` during replay. After replay it checks that the event table
  is readable and returns 200 `{"status":"ready","last_seq":0}` (the sequence is zero for an empty log).
  A failed database check returns 503 `{"status":"unavailable","error":"..."}`. Use this for readiness.
- Other routes return 503 with `Retry-After: 1` until replay completes.

## Backup

Run the backup command where the database is mounted, even while `serve` runs:

```sh
mkdir -p backups
metronome backup --db metronome.db --to backups/metronome-$(date -u +%Y%m%dT%H%M%SZ).db
```

It produces a verified, consistent SQLite snapshot including committed WAL writes, reports the event count
and last sequence, and refuses an existing destination. The destination filesystem must support hard links.
There is no HTTP backup endpoint. Arrange retention and off-volume copies in the operator's infrastructure.

## Restore

Stop the service and its writers before restoring. Restoring discards events after the backup. Never copy a
backup over a running service's database. Move the old database and any WAL and SHM files into a new directory
so the restored database cannot encounter stale sidecars. Preserve them for inspection: a WAL after a crash
may contain committed events.

```sh
mkdir replaced
mv metronome.db replaced/
for sidecar in metronome.db-wal metronome.db-shm; do
  if [ -e "$sidecar" ]; then mv "$sidecar" replaced/; fi
done
cp backups/metronome-YYYYMMDDTHHMMSSZ.db metronome.db
metronome serve --db metronome.db
```

Use the actual backup filename and choose a fresh replacement directory if `replaced` already exists.
In a separate terminal, check `curl -s http://127.0.0.1:7340/readyz` once replay finishes. The returned
`last_seq` should match the sequence reported by the backup command. Resume the intake, publish and poll
loops after verifying the restore.
