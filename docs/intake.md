# Writing intake and publish commands

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

The publisher command uses its own credentials to mark claims and clear its own markers when work finishes.
Metronome's core never writes to the source itself. When reading the source, set `claimed_by` to a nonempty claimant name for items claimed
by somebody else, and leave it empty or absent for unclaimed items and this instance's own markers.

```json
{"items":[{"ref":"ACME-3","title":"Export records","owner":"pat","claimed_by":"laptop-b"}]}
```

Both local and hosted instances apply the same claim rule. Intake skips foreign claims and reports them;
it shelves already-Ready items claimed elsewhere. An item in Plan or later remains held locally and a
person must resolve a conflicting claim. A periodic snapshot is not a distributed lock: two instances can
take an unmarked item before either publishes its marker. Resolving that race remains open.

For GitHub, give each installation a distinct `--instance` name, and use that same name with both
`github issues` and `github publish`. The publisher adds `metronome:claimed:INSTANCE` on the first slice
claim. Intake treats every such label except its own as a foreign claim (without `--instance`, all are
foreign). Assignees alone do not indicate a claim. The publisher removes its label on done or dropped,
and removes the assignee only if it added that assignment itself. This works for several local instances,
or local and hosted instances sharing one source; it does not eliminate the race before a marker appears.

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
displace existing Ready items. Changes to owner, value, tier, labels or slices on existing Ready or restored items
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
| `claimed_by` | Sorted, comma-separated instance names from foreign `metronome:claimed:INSTANCE` labels. |
| `predicted_tier` | A `tier:N` label (0–3), omitted when absent. |
| `slices` | One slice with the same title and repository, no dependencies or named approvers. |
| `criteria` | List items in an Acceptance criteria heading's section, otherwise markdown task-list items. Wrapped text is joined, including checked tasks. |

Pull requests, non-issue or unreadable project items, and project items from other repositories are skipped.
Missing owners, invalid or conflicting tier labels, duplicate refs, missing or nonnumeric project fields,
and failed reads exit nonzero without printing backlog JSON. With no matching issues, `items` is empty.
The built-in intake command is read-only, reads claims written by `github publish`, and does not contact the Metronome service.

## Built in: Azure DevOps

```sh
metronome ado items --org https://dev.azure.com/example --project acme --wiql ready.wiql --owner pat
metronome ado items --org https://dev.azure.com/example --project acme --query 12345678-1234-1234-1234-123456789abc --owner pat
metronome intake --workstream default -- metronome ado items --org https://dev.azure.com/example --project acme --wiql ready.wiql --owner pat
```

Supply a flat WIQL query in a file or the GUID of a saved flat query, not both. Install the Azure DevOps
extension for `az` and sign in via `az login` or `az devops login`; credentials stay with the CLI.
The command uses these read-only requests (the POST bodies are in private temporary files):

```sh
az devops invoke --org ORG --area wit --resource wiql --route-parameters project=P --http-method POST --in-file TMP --api-version 7.1 --detect false --output json
az devops invoke --org ORG --area wit --resource wiql --route-parameters project=P id=GUID --http-method GET --api-version 7.1 --detect false --output json
az devops invoke --org ORG --area wit --resource workitemsbatch --route-parameters project=P --http-method POST --in-file TMP --api-version 7.1 --detect false --output json
```

The first body is `{"query":"<WIQL text>"}`. The batch body is
`{"ids":[1,2],"$expand":"relations","errorPolicy":"fail"}`. Details are read in batches of at most 200 IDs.
Ranked items come first, lowest rank first, with ties broken by ID; unranked items come last.

| Backlog field | Work item mapping |
| --- | --- |
| `ref` | `AB#ID` from `id`. |
| `title` | `System.Title`. |
| `intent` | First prose paragraph of `System.Description`, converted from HTML to plain text and capped at 500 runes. |
| `owner` | `System.AssignedTo.uniqueName`, then `displayName`, otherwise `--owner`. |
| rank | `Microsoft.VSTS.Common.BacklogPriority`, falling back to `Microsoft.VSTS.Common.StackRank`. |
| `predicted_tier`, `labels` | Semicolon-separated `System.Tags`: `tier:N` (0–3) sets tier, other distinct tags are labels. |
| `slices` | One slice if Description or Acceptance Criteria has a standalone `Repo: OWNER/NAME` line; otherwise no pre-planned slices. |
| `criteria` | HTML list items from `Microsoft.VSTS.Common.AcceptanceCriteria`, or its paragraphs if it has no list items. |
| `claimed_by` | Assignee, or `state STATE` for non-ready, unfinished `System.State`. |

HTML entities are decoded after tag removal, so escaped angle brackets stay literal text. Repository
markers are excluded from intent and criteria. Value stays zero and approvers are empty.

By default only unassigned items in `New`, `Approved`, `To Do` or `Proposed` are offered. Override the
ready-state list with `--ready-states 'New,Ready'` (case-insensitive). Assigned and other unfinished
items, including `Committed`, remain in the result with claims so intake skips them. `Done`, `Closed`
and `Removed` are always omitted and noted on stderr, even if named in the ready-state list.

Predecessor relations (`System.LinkTypes.Dependency-Reverse`) are reported on stderr whether inside or
outside the query result; finished predecessors in the result need no note. Cross-item dependencies
are not yet supported (#46), so these links do not gate claims. Choose a query that offers only work
whose prerequisites are met.

Failed reads, link queries, missing or duplicate IDs, invalid fields, missing owners, conflicting tier
or repo markers fail without printing backlog JSON. A complete empty flat result prints `{"items":[]}`.
The source is read-only, publishes no claims, and does not contact the Metronome service. The az invoke
resource names and response shapes have been tested against fixtures, not a live Azure DevOps organisation.

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

## Publishing back to the source

```sh
metronome publish --workstream example --name board --every 1m -- ./publish-to-source.sh
```

The named workstream must exist. Each name has a durable cursor scoped to that workstream. A new name
starts at the beginning of the log. Use one running command per name; a concurrent cursor advance is
rejected, but commands may already have made external changes. The service hands the publisher transitions
in batches of at most `--batch` (default 100, range 1–1000), except that transitions from one event are never
split across batches. One round drains available batches and never invokes a command for an empty batch.
Without `--every`, or with `--every 0`, it runs once. A positive interval runs immediately and periodically
until interrupted. Do not use `--request-id`.

The command reads one JSON object per line on stdin. Its stdout and stderr pass through. Only a successful
exit with valid publisher state advances the cursor. Failure leaves the cursor unchanged, and the next round
offers the same transitions again (at-least-once delivery). Periodic runs continue after failures; one-shot
runs exit nonzero. Commands must make repeated delivery safe.

Along with the caller's environment, the command receives:

- `METRONOME_WORKSTREAM`: the workstream name.
- `METRONOME_PUBLISHER`: the publisher name.
- `METRONOME_PUBLISHER_STATE`: a private temporary file containing the publisher's opaque JSON state, initially
  `null`. Write a replacement JSON value (at most 256 KiB) here, preferably by atomic rename. The service commits
  valid changed state even when the command fails or is interrupted, without advancing the cursor in that case.
  Saving state has a separate five-second timeout so an interrupted round can finish recording its progress. The file is removed
  after the round. Never put credentials in it: state is stored in the event log.

## Transition contract

The stream is additive-only: ignore unknown fields and transition names. Names describe state changes, not
internal events. Ready items added, shelved or restored by intake do not produce transitions. Sort by emission
order, not timestamp; sequence values can repeat for several transitions caused by one event. Deduplicate
using `(seq, transition, slice_id)` within a workstream's stream. Slice changes precede item changes for the
same event; ask changes follow them.

```json
{"seq":42,"at":"2026-11-27T10:00:00Z","workstream":"example","item_id":"I3","ref":"acme/app#7","slice_id":"I3.1","transition":"pr_open","from":"submitted","pr":{"repo":"acme/app","number":12,"head":"abc123"},"actor":"github"}
```

| Field | Meaning |
| --- | --- |
| `seq`, `at` | Log sequence and event time (RFC 3339). |
| `workstream`, `item_id`, `ref` | Workstream, local item ID and source reference. |
| `transition`, `from` | Transition name; previous status (omitted for asks). |
| `actor` | Who recorded the change. |
| `slice_id` | Present for slices and asks about slices. |
| `pr` | When known for a slice: `repo`, `number`, `head`. |
| `runner`, `branch` | Runner on claim; branch on submission. |
| `ask` | On ask transitions: `id`, `type`, `question`, `owner`, and `answer` when answered. |

| Transition | Meaning |
| --- | --- |
| `planning`, `ready` | Plan started or returned to Ready. |
| `executing` | Plan approved or release approval withdrawn. |
| `awaiting_release`, `releasing` | Waiting for or holding a Release slot. |
| `done`, `dropped` | Item finished or abandoned. |
| `claimed`, `submitted` | Slice claimed or submitted. |
| `pr_open`, `in_review`, `approved`, `merged` | PR stages, including return to PR open on approval withdrawal. |
| `rework` | Failed checks or requested changes. |
| `stalled`, `parked`, `flagged` | Slice waiting, returned to queue, or flagged. |
| `asked`, `answered`, `ask_timed_out` | Human ask raised, resolved or timed out. |

## Writing a publisher

A publisher owns the source workflow and credentials. For example, this command consumes the stream and
prints the source reference and transition for another integration to handle:

```bash
#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r transition; do
  printf '%s\n' "$transition" | jq -er '[.ref, .transition] | @tsv'
done
```

An actual source adapter should exit nonzero on a failed write, keep its own last-written values in the state
file, and use repeat-safe source operations. Do not acknowledge partial work as complete.

## Built in: GitHub Project publisher

```sh
metronome publish --workstream example --name board --every 1m -- metronome github publish --project acme/3 --instance laptop-a
```

The command uses `gh` and requires project write access (`gh auth refresh -s project`) and issue write access.
It resolves issues by `OWNER/NAME#N` or by a unique `Metronome: REF` line in a project issue's body. Unmatched
or ambiguous body refs are reported and skipped. An issue addressed directly outside the project can still
receive claims, PR comments and closure, but its Status is skipped. Failed reads or writes and missing project
fields or options fail the round. `--status-field` names the single-select field (default `Status`).

| Default Status | Transitions |
| --- | --- |
| Ready | `planning`, `ready` |
| In progress | `claimed`, `submitted`, `pr_open`, `rework` |
| In review | `in_review`, `approved` |
| Done | `done` |

All others leave Status alone. Use repeatable `--map TRANSITION=OPTION` to override the mapping, or
`--map TRANSITION=` to unmap one. For items with several slices, the latest mapped transition wins.
The publisher reads the board at the start of each invocation. It stores the last Status it wrote (or adopted
when already equal) next to the cursor, not in a GitHub field or a machine-local file. If the board differs
from that value, it leaves the human edit alone, even if it happens to match the next mapped target. On the
first observation a matching target can be adopted without a write. This protects
edits between runs; a human edit racing a write within the same round is not an atomic compare-and-set.

The first record carrying a distinct PR adds a comment linking it, with a marker to prevent duplicate comments
on retries. `claimed` adds the instance label and assigns the issue to `--assignee` (default the `gh` login).
On `done` the issue is closed as completed; `done` and `dropped` clear this instance's claim. Completed
transition keys are kept in publisher state to avoid replaying successful operations in a partially failed batch.
Running `github publish` directly without the state file is supported but warns that last-written values will
not persist; use `metronome publish` for normal operation.
