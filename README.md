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
replays it. `metronome github poll` reads each submitted slice's PR through `gh` and records what GitHub shows:
the PR opening, checks on its head, review and merge. The same events can still be entered by hand
(`metronome github …`).

Each item belongs to one workstream, with independent Ready, Plan, Execute, Review and Release limits.
The approver cap remains global per person. Existing logs and backlog syncs without a workstream use
`default`. A zero stage limit prevents new slot allocations in that stage; existing slots stay held.
Ranks are local to each workstream. Without a filter, planning and claims choose eligible work across
workstreams, and the board shows a section for each workstream.

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
./metronome next --runner laptop             # resumes the answered slice: run R2
./metronome submit R2 --branch metronome/I1.1
./metronome --as github github poll --every 1m  # follows the PR on that branch until it merges
./metronome board
./metronome workstream create example --execute 0
./metronome backlog sync --workstream example source.json
./metronome workstream set example --execute 2
./metronome workstream list
./metronome board --workstream example
```

Run `metronome help` for every command.

## Developing

```sh
go vet ./...
go test ./...
```

## License

[Apache-2.0](LICENSE)
