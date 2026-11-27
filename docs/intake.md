# Writing an intake command

The source owns the backlog. Metronome holds the Ready queue and the record of work in progress.
Run a command that reads the source using its own credentials:

```sh
metronome intake --workstream example -- ./backlog-from-source.sh
metronome intake --workstream example --every 10m -- ./backlog-from-source.sh
```

The workstream must already exist. With a positive `--every`, intake runs immediately, then periodically,
one round at a time, until interrupted (Ctrl-C or SIGTERM). Without it, or with `--every 0`, it runs once.
Do not use `--request-id`: each round is idempotent by content rather than by reusing a request ID.

## Command contract

Print exactly one [backlog JSON object](backlog.md) on stdout, with a non-null `items` array in rank order.
The optional `workstream` must match the flag when supplied. An empty array means the source has no items;
missing or null `items`, unknown fields, duplicate refs, and invalid items are refused. Use stderr for logs
and exit non-zero if the source could not be read completely. Never print a partial list as a successful
result: incomplete is never clean.

The command inherits the caller's environment and receives:

- `METRONOME_WORKSTREAM`: the target workstream name.
- `METRONOME_ITEMS`: path to a temporary JSON file listing every recorded item in that workstream, ordered
  by ID. It is removed after the round. The command can use this snapshot to recognise and publish claims.

```json
{
  "workstream": "example",
  "items": [
    {"ref": "ACME-1", "id": "I1", "status": "planning"},
    {"ref": "ACME-2", "id": "I2", "status": "ready"}
  ]
}
```

Failures starting or running the command, invalid output, and rejected syncs are reported. Command stderr
passes through. No backlog changes are made for failed command runs or invalid output. A single failed
round exits nonzero; periodic intake continues after errors. Interrupting periodic intake exits zero.

## Shared sources and claims

The source command may use its own credentials to publish claims (for example, an assignee or an
instance-specific label) for items in Plan or later, using `METRONOME_ITEMS`. It can also use the snapshot
to close or hide completed work, or clear its own markers when appropriate. Metronome never writes to
the source itself. When reading the source, set `claimed_by` to a nonempty claimant name for items claimed
by somebody else, and leave it empty or absent for unclaimed items and this instance's own markers.

```json
{"items":[{"ref":"ACME-3","title":"Export records","owner":"pat","claimed_by":"laptop-b"}]}
```

Both local and hosted instances apply the same claim rule. Intake skips foreign claims and reports them;
it shelves already-Ready items claimed elsewhere. An item in Plan or later remains held locally and a
person must resolve a conflicting claim. A periodic snapshot is not a distributed lock: two instances can
take an unmarked item before either publishes its marker. Resolving that race remains open.

## Mirror rules

| Local item status | Intake behaviour |
| --- | --- |
| Unknown | Add unclaimed items in source order when Ready has room. |
| Ready | Update rank, title, intent and labels; shelve items missing from the list or claimed elsewhere. |
| Shelved | Restore the same item ID when listed unclaimed and Ready has room. |
| Planning, executing, awaiting release, releasing | Keep unchanged; report a disappearance or foreign claim. |
| Done or dropped | Ignore. |
| In another workstream | Report and skip. |

Rank is the item's 1-based position in the full source list, including skipped items. Previously parked
items retain their front-of-queue priority. Shelving frees Ready room before refill. Adds and restores
occur only when Ready is below the refill threshold, up to the workstream's Ready limit. New items never
displace existing Ready items. Changes to owner, value, tier or slices on existing Ready or restored items
are reported rather than applied. Shelved items retain the reason and claimant recorded when shelved;
they are visible through `board --json`, but not the text board or planning queue.

Running the same complete source output twice against unchanged tracker state emits no new events on the
second round. Notes may repeat. `POST /v1/backlog/intake` offers the same mirror rules to API callers and
requires both `workstream` and `items`. The older `backlog sync` remains add-only, skipping foreign claims.

## Built in: GitHub Issues

```sh
metronome github issues --repo acme/app --owner pat
metronome intake --workstream metronome --every 10m -- metronome github issues --repo OWNER/NAME --project OWNER/N --rank-field Rank
```

Create the workstream before running intake. The command reads all open issues with `--label` (default
`metronome`) through `gh`, using whoever `gh` is signed in as. With `--project OWNER/NUMBER`, it reads
the project's numeric `--rank-field` (default `Rank`); project access requires the `read:project` scope
(`gh auth refresh -s read:project`). Ranked issues come first in ascending rank order; unranked issues
come last. Ties and issues without a project are ordered oldest first, then by issue number.

| Backlog field | Issue mapping |
| --- | --- |
| `ref` | `OWNER/NAME#N`, or the token in a standalone `Metronome: REF` line outside fenced code. Use the original ref to migrate existing backlog items. |
| `title` | Issue title. |
| `intent` | First prose paragraph, condensed to one line and capped at 500 characters; empty for no body. |
| `owner` | First assignee's login, otherwise `--owner`. |
| `predicted_tier` | A `tier:N` label (0–3), omitted when absent. |
| `slices` | One slice with the same title and repository, no dependencies or named approvers. |
| `criteria` | List items in an Acceptance criteria heading's section, otherwise markdown task-list items. Wrapped text is joined, including checked tasks. |

Pull requests, non-issue or unreadable project items, and project items from other repositories are skipped.
Missing owners, invalid or conflicting tier labels, duplicate refs, missing or nonnumeric project fields,
and failed reads exit nonzero without printing backlog JSON. With no matching issues, `items` is empty.
The built-in command is read-only, publishes no claims, and does not contact the Metronome service.

## Example: writing another source command

This Bash command reads issues in the `ready` list. It assumes `pat` is responsible for the work and that
any assignee other than `pat` denotes a foreign claim. Adapt that convention to your source; a shared
account needs instance-specific markers instead. The example is read-only; publishing claims is the
source adapter's responsibility. Fail closed if the configured limit may truncate the source list.

```bash
#!/usr/bin/env bash
set -euo pipefail
gh issue list --repo acme/app --label ready --limit 1000 --json number,title,body,assignees |
  jq 'if length >= 1000 then error("source may be truncated") else
        {items: map({ref: ("ACME-" + (.number | tostring)), title, intent: .body,
                     owner: "pat", claimed_by: ([.assignees[].login | select(. != "pat")] | join(","))})}
      end'
```

If more issues exist than the command can read, treat that as a failure or paginate to completion rather
than returning a truncated list.
