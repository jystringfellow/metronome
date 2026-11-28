# Metronome

A control plane for AI-driven delivery work. Agents, local or cloud, pull work through one claim protocol, and
WIP limits keep planning and execution within what a team can review and release. People set the pace through a
flag and an inbox, and deterministic rules owned by each repository decide approvals.

Metronome is a design and an early proof of concept.

- [SPEC.md](SPEC.md): the full design.
- [DECISIONS.md](DECISIONS.md): what has been decided, what the design review found, and what is still open.
- [docs/runners.md](docs/runners.md): how agents take work and hand it back.
- [docs/backlog.md](docs/backlog.md): the backlog JSON contract.
- [docs/intake.md](docs/intake.md): writing source commands for scheduled intake and publishing.
- [docs/operations.md](docs/operations.md): health checks, database backups and restore.

## What runs today

Build step 1 of the SPEC, local only: one `metronome` binary holding an HTTP service, a CLI and an append-only
event log in SQLite. Items, slices, runs, slots and asks are all derived from the log, so restarting the service
replays it. `metronome github poll` reads each submitted slice's PR through `gh` and records what GitHub shows:
the PR opening, checks on its head, review and merge. It also returns a slice waiting for rework to review
when its recorded PR gets a new green head outside a run. The same events can still be entered by hand
(`metronome github …`, including `metronome github rework-fixed`).

Adopt an existing same-repository PR with `metronome github adopt acme/app#12 --workstream default`, or
configure `metronome workstream set default --adopt-label metronome:adopt` and run
`metronome github poll --adopt-repo acme/app`. A Ready source item naming a PR via a `PR:` line or linked
GitHub PR is also adopted by the poller instead of planned. Adoption reserves Review capacity, follows the
PR's own branch, and is idempotent. Forks are refused. Rework runners update the shared branch first;
see [adopted PRs](docs/runners.md#adopted-prs).

Each item belongs to one workstream, with independent Ready, Plan, Execute, Review and Release limits.
The approver cap remains global per person. Existing logs and backlog syncs without a workstream use
`default`. A zero stage limit prevents new slot allocations in that stage; existing slots stay held.
Ranks are local to each workstream. Without a filter, planning and claims choose eligible work across
workstreams, and the board shows a section for each workstream.

A slice may list `after`, the 1-based positions of slices of the same item that must merge first.
Until they do, claims skip it, and `board` shows what it waits on.

Backlog items may carry pre-planned slices. `plan start` proposes them automatically and asks the item's
owner to approve. If the backlog supplies no predicted tier, tier 1 is proposed for confirmation.

`metronome intake` runs a source command once or on a schedule and mirrors its ranked backlog into a
workstream's Ready queue. The command owns source access and reports claims held by other instances.
Unlike add-only `backlog sync`, intake updates Ready items and shelves those missing or claimed elsewhere;
it never discards work in Plan or later.

`metronome github issues` is a read-only intake source for open labelled GitHub issues. It uses `gh`,
optionally ranks issues by a GitHub Project's numeric field, and supplies one pre-planned slice per issue.
Plans still need their owner's approval. See [intake](docs/intake.md) for the mapping and flags.

`metronome ado items` reads flat Azure Boards queries through `az`, ranks work items, and maps their
owners, HTML descriptions, acceptance criteria, tags and repository markers. Assigned and non-ready
work is skipped by intake. See [Azure DevOps intake](docs/intake.md#built-in-azure-devops).

`metronome publish` mirrors workstream transitions back to a source command with a durable cursor.
`metronome github publish` updates a GitHub Project Status without replacing a value changed by a person
between runs, comments with PR links, marks claims and closes done issues. See the
[publish contract](docs/intake.md#publishing-back-to-the-source).

For one shared source, run the two commands (in separate terminals) with the same instance name:

```sh
metronome workstream create example
metronome intake --workstream example --every 10m -- metronome github issues --repo acme/app --project acme/3 --instance laptop-a --owner pat
metronome publish --workstream example --name board --every 1m -- metronome github publish --project acme/3 --instance laptop-a
```

```sh
go build -o metronome ./cmd/metronome
export PATH="$PWD:$PATH"
./metronome serve --db metronome.db &

./metronome backlog sync backlog.json        # {"items":[{"ref":"ACME-1","title":"…","intent":"…","owner":"pat"}]} (optional labels, predicted_tier and slices; see docs/backlog.md)
./metronome plan start
./metronome plan propose I1 --slices slices.json   # [{"title":"…","repo":"acme/app","criteria":["…"],"approvers":["rev"]},{"title":"…","repo":"acme/app","after":[1]}]
./metronome --as pat plan approve I1

./metronome next --runner laptop             # claims a slice and prints its brief: run R1, lease 15 minutes
./metronome report R1 --note "tests written" # renews the lease
./metronome ask --run R1 --type answer --question "CSV or JSON?"
./metronome inbox pat
./metronome answer F2 "CSV"
./metronome next --runner laptop --json      # resumes the answered slice as JSON: run R2
./metronome submit R2 --branch metronome/I1.1
./metronome --as github github poll --every 1m  # follows the PR on that branch until it merges
./metronome board
./metronome workstream create example --execute 0
./metronome backlog sync --workstream example source.json
./metronome intake --workstream example --every 10m -- ./backlog-from-source.sh
./metronome workstream create metronome
metronome intake --workstream metronome --every 10m -- metronome github issues --repo OWNER/NAME --project OWNER/N --rank-field Rank
metronome intake --workstream default -- metronome ado items --org https://dev.azure.com/example --project acme --wiql ready.wiql --owner pat
./metronome workstream set example --execute 2
./metronome workstream list
./metronome board --workstream example
./metronome board --label slow
```

Run `metronome help` for every command.

While `serve` runs, open <http://127.0.0.1:7340/> for a read-only board with workstream lanes, stage limits
and usage, live lease countdowns, open asks and readable recent activity. Switch workstreams or filter by a
label using links that update the page address, without changing capacity totals. All workstreams' live
work appears before the compact, grouped Ready and shelved backlogs. The page refreshes every few seconds,
highlights changes, ships inside the binary and changes nothing. The page and API are unauthenticated;
keep the service on loopback.

For a hosted service, keep the board and API behind an authenticating proxy. See
[running as a service](docs/operations.md) for probes and online backups.

To wrap a coding agent and keep its lease alive until it finishes:

```sh
./metronome run --runner laptop -- sh -c 'claude -p "Do the work in $METRONOME_BRIEF" && git branch --show-current > "$METRONOME_BRANCH_FILE"'
```

The command receives the claim and service details in `METRONOME_*` environment variables (see
[DECISIONS.md](DECISIONS.md)). The brief file contains the same JSON as `next --json`. By default the wrapper
renews the lease every five minutes (`--every` overrides this), submits on success with a branch in
`METRONOME_BRANCH_FILE`, and otherwise drops the run, keeping the slice's slots. A command that already closed
its run is left alone. Metronome prints its own messages to stderr.

## Developing

```sh
go vet ./...
go test ./...
```

## License

[Apache-2.0](LICENSE)
