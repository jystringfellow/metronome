# Metronome

A control plane for AI-driven delivery work. Agents, local or cloud, pull work through one claim protocol, and
WIP limits keep planning and execution within what a team can review and release. People set the pace through a
flag and an inbox, and deterministic rules owned by each repository decide approvals.

Metronome is a design and an early proof of concept.

- [SPEC.md](SPEC.md): the full design.
- [DECISIONS.md](DECISIONS.md): what has been decided, what the design review found, and what is still open.

## What runs today

Build step 1 of the SPEC, local only: one `metronome` binary holding an HTTP service, a CLI and an append-only
event log in SQLite. Items, slices, runs, slots and asks are all derived from the log, so restarting the service
replays it. GitHub events are entered by hand for now (`metronome github …`); a `gh` poller is next.

```sh
go build -o metronome ./cmd/metronome
./metronome serve --db metronome.db &

./metronome backlog sync backlog.json        # {"items":[{"ref":"ACME-1","title":"…","owner":"pat","predicted_tier":1}]}
./metronome plan start
./metronome plan propose I1 --slices slices.json   # [{"title":"…","repo":"acme/app","approvers":["rev"]}]
./metronome --as pat plan approve I1

./metronome next --runner laptop             # claims a slice: run R1, lease 15 minutes
./metronome report R1 --note "tests written" # renews the lease
./metronome ask --run R1 --type answer --question "CSV or JSON?"
./metronome inbox pat
./metronome answer F2 "CSV"
./metronome board
```

Run `metronome help` for every command.

## Developing

```sh
go vet ./...
go test ./...
```

## License

[Apache-2.0](LICENSE)
