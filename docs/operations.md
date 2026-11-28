# Running Metronome as a service

Run one `metronome serve` instance with PostgreSQL or a SQLite database on persistent storage. Keep the unauthenticated
board and API behind an authenticating proxy; probes can reach the service directly.

PostgreSQL is recommended for hosted instances. SQLite remains the fully supported default for a laptop
or single small host; no PostgreSQL installation is required. Select the store using `--db` or `--db-env`.

For Kubernetes installations, see the [Helm chart guide](kubernetes.md).

## PostgreSQL

Set `METRONOME_DB` to the connection URL using your secret store, and start Metronome with:

```sh
metronome serve --db-env METRONOME_DB --config instance.json
```

Using `--db-env` keeps the URL out of process arguments; Metronome does not print connection credentials
or the URL. Give the instance its own database or an existing dedicated schema (set `search_path` in the URL).
Use a `postgres://` or `postgresql://` URL; keyword/value connection strings and malformed URL schemes
are rejected rather than interpreted as file paths.
The database role needs permission to create tables and write events in that schema. Use a direct or
session-preserving connection, not transaction pooling. Schema setup is automatic and versioned. Configure
TLS according to your database provider, for example with `sslmode=verify-full`.

Only one writer may hold the database/schema's advisory lock. A second instance refuses to start, so use
a stop-then-start (Recreate) rollout, not overlapping replicas. If a connection is lost, Metronome reconnects
and takes the lock again before accessing the log. If another writer holds the lock or the log sequence
or schema version has changed, readiness fails and the instance refuses further writes. Restart it to replay the log.

Use your database provider's backups and point-in-time restore, or PostgreSQL's `pg_dump` and `pg_restore`.
Stop the instance and its writers before restoring and verify `/readyz` afterwards. The `metronome backup`
command is for SQLite only.

## Moving from SQLite

Stop the SQLite service and all its writers, including intake, publish and poll loops. Set `METRONOME_DB`
to the destination PostgreSQL URL. The destination must be empty or contain an exact prefix of the source log.

```sh
metronome migrate-store --from metronome.db --to-env METRONOME_DB
metronome serve --db-env METRONOME_DB
```

Migration preserves the event sequence, timestamps and payloads and verifies the copied events and replayed
state. Rerunning the command copies only missing events. Keep the original SQLite file as a backup.
Check `/readyz` and ensure `last_seq` equals the sequence reported by the migration before resuming the loops.

## Probes

For a hosted instance, use `metronome serve --config instance.json` with a database on persistent storage
and an authenticating proxy in front of the service. See [instance configuration](configuration.md).

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

## SQLite backup

Run the backup command where the database is mounted, even while `serve` runs:

```sh
mkdir -p backups
metronome backup --db metronome.db --to backups/metronome-$(date -u +%Y%m%dT%H%M%SZ).db
```

It produces a verified, consistent SQLite snapshot including committed WAL writes, reports the event count
and last sequence, and refuses an existing destination. The destination filesystem must support hard links.
There is no HTTP backup endpoint. Arrange retention and off-volume copies in the operator's infrastructure.

## SQLite restore

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

For a configuration-managed instance, verify the restore without `--config` first, then stop and restart
with `--config` to resume the loops.
