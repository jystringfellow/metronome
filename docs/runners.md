# Running work from Metronome

This guide is for a coding agent told to take work from Metronome, and for the person wiring that agent up.
The runner needs the `metronome` CLI and access to a running `metronome serve`.

Set `METRONOME_URL` to the service URL (default `http://127.0.0.1:7340`) and `METRONOME_ACTOR` to the actor
doing the work (default `$USER`). The global flags `--server URL` and `--as WHO` override these values.
Global flags go before the command. Use `--request-id ID` to make a command safe to repeat. Use a new ID
for a new operation.

## The loop

1. Claim a slice.
2. Read its brief and the repository's instructions.
3. Do the work, check the acceptance criteria and keep the lease alive.
4. If blocked on a human decision, ask and stop working on that run.
5. Submit the work on a branch, or drop the run if you cannot finish.

## Claiming

```sh
metronome next --runner laptop
```

`--runner ID` is required. To request a particular slice, add `--slice I1.1`. It must be queued or stalled.
The tracker claims rework first, then slices already holding slots, then parked slices returned to the front,
then new slices by backlog rank. A claim needs an Execute slot and a Review reservation. It reuses any slots
the slice already holds. If nothing fits, the command fails with a no-work or capacity error. Do not start
work without a successful claim.

The text output looks like this (the lease time is local):

```text
run R1 claimed slice I1.1; lease until 12:15PM
slice:     I1.1 Export records
repo:      acme/app
item:      ACME-1 Add export (I1)
intent:    Let people save their records
criteria:
  - Records can be saved as a file
resume:    no earlier run
```

For a machine-readable brief, use this instead of the text command. Both commands claim work.

```sh
metronome next --runner laptop --json
```

```json
{
  "claim": {
    "run_id": "R1",
    "slice_id": "I1.1",
    "lease_until": "2026-11-27T12:15:00Z"
  },
  "slice": {
    "id": "I1.1",
    "repo": "acme/app",
    "title": "Export records",
    "criteria": ["Records can be saved as a file"],
    "approvers": []
  },
  "item": {
    "id": "I1",
    "ref": "ACME-1",
    "title": "Add export",
    "intent": "Let people save their records"
  },
  "history": {
    "reason": "",
    "rework": false,
    "rework_reason": "",
    "failed_head": "",
    "changes_requested_by": "",
    "previous_run": "",
    "branch": "",
    "pr": null,
    "asks": []
  }
}
```

- `claim`: the run ID for reporting, asking, submitting or dropping, the slice ID and the lease deadline
  (RFC 3339 in UTC).
- `slice`: the work to deliver, its repository, title, acceptance criteria and named approvers.
- `item`: the parent item's ID, backlog reference, title and intent.
- `history`: a snapshot of why the slice is claimable, its rework status, previous run and recorded branch
  and PR, and answered asks. It is fixed at claim time, including when a claim is repeated with the same request ID.

All JSON fields are present. Empty lists are `[]` and missing intent is `""`. Changes to the JSON contract
are additive only. Text output says `none given` for missing intent or criteria and omits empty approvers.

The `history.reason` field explains why work is available again:

| Reason | Meaning |
| --- | --- |
| `""` | No earlier interruption or rework. |
| `checks_failed` | Checks failed on the head in `failed_head`. |
| `changes_requested` | Review requested changes; `changes_requested_by` names the reviewer if recorded. |
| `lease_expired` | The previous run's lease expired. |
| `dropped` | The previous run was dropped. |
| `answered_ask` | The last open ask on the flagged slice was answered. |
| `ask_timed_out` | An unanswered ask timed out and the slice parked. |

`rework` is true after failed checks or a change request. `rework_reason` and its associated `failed_head`
or `changes_requested_by` persist until the next submit, even if a subsequent interruption changes `reason`.
`previous_run` is the previous run ID and `branch` is the last submitted branch, or `""` when absent.
`pr` contains `repo`, `number` and `head_sha` for the recorded PR, or is `null` when absent. `asks` lists
resolved asks on this slice in raised order, with `id`, `type`, `question`, `answer`, `answered_by` (empty
if not recorded) and `answered_at` (RFC 3339 UTC). Open asks, timed-out asks and item-level asks are excluded.
Text output shows the resume reason and, when applicable, rework, previous work and answered asks.

The brief is not the full context bundle. If `next` claims a slice but cannot read the brief, it fails and names the run.
Drop that run rather than starting work with a partial brief.

## While working

### Report and renew

```sh
metronome report R1 --note "Tests written"
```

The note is optional. Reporting renews the lease, which is 15 minutes in the proof of concept. Report before
each deadline. Once the lease expires, the next service tick closes the run with reason `lease_expired`
(`serve --tick` defaults to 30 seconds). The slice is stalled and keeps its slots. Another runner can claim
it. After the stall timeout (currently two hours), an unclaimed stalled slice parks and frees its slots.

### Ask a person

```sh
metronome ask --run R1 --type answer --question "CSV or JSON?"
```

The general form is `metronome ask (--run R | --item I | --slice S) --type ASK --question TEXT [--owner WHO]`.
Give exactly one subject. For an open run, use `--run`, not `--slice`. The latter fails if the slice has an
open run. The ask goes to the item's owner unless you supply `--owner`.

| Type | When to use it |
| --- | --- |
| `answer` | An open question or ambiguity blocks a runner. This is the usual runner ask. |
| `decide` | A person must choose a priority or course of action, such as rollback or fix forward. |
| `approve` | A person must approve a plan, PR or rollout. |
| `verify` | A person must judge something automation cannot, such as user acceptance. |

A runner rarely needs the last three types. They do not replace the normal approval or verification gates.

Asking through a run closes it with reason `flagged`. The slice waits in `flagged` status and keeps its slots.
Stop working on that run. The person can see and answer the ask in their inbox, for example:

```sh
metronome inbox pat
metronome answer F2 "CSV"
```

Use the flag ID from the inbox. When the last open ask on the flagged slice is answered, the slice becomes
stalled. A new claim can resume it with a new run ID. The answer is in the new brief's `history.asks`.
An unanswered `answer` ask times out after 24 hours in the proof of concept. The slice parks and frees its
slots without an answer. Other ask types stay open past their timeout. A timeout is not a decision.

### Drop

```sh
metronome drop R1
```

Drop a run you cannot finish and do not need to ask about. It closes the run and leaves the slice stalled,
holding its slots. Another claim can resume it. Do not keep working under the old run ID.

### Submit

```sh
metronome submit R2 --branch metronome/I1.1
```

Use the current run ID. `--branch` is required. The work must arrive on that branch in the slice's repository.
`metronome/<slice id>` is a naming convention, not a requirement. Submit closes the run as submitted. It does
not push the branch, open the PR, approve it or merge it.

A person or service runs the GitHub poller, with `gh` signed in:

```sh
metronome --as github github poll --every 1m
```

Without `--every`, the poller runs once. It follows the PR on the submitted branch and records the PR opening,
checks on its head, review and merge. A change request that sends the slice back to Execute makes it available
for another claim as rework. The new brief identifies who requested changes in `history.changes_requested_by`.
The runner must not enter GitHub events by hand.

## Wrap an agent with `metronome run`

Use `metronome run --runner ID [--slice S] [--every 5m] -- CMD [ARGS...]` to let Metronome manage the claim,
lease and handoff. `--runner` is required, as are `--` and a command. `--every` must be a positive duration.
The default is five minutes. The global `--request-id` applies only to the claim.

The wrapper claims as `next` does, prints the text brief to stderr and starts the command. The command keeps
stdin, stdout and stderr. Metronome's own messages go to stderr. It inherits the environment with these
overrides:

| Variable | Value |
| --- | --- |
| `METRONOME_RUN_ID` | Claimed run ID. |
| `METRONOME_SLICE_ID` | Claimed slice ID. |
| `METRONOME_RUNNER` | Runner ID from `--runner`. |
| `METRONOME_BRIEF` | Absolute path to a file containing the same JSON as `next --json`. |
| `METRONOME_BRANCH_FILE` | Absolute path to an initially empty file for the branch name. |
| `METRONOME_URL` | Service URL in effect. |
| `METRONOME_ACTOR` | Actor in effect. |

The environment contract is additive only. Both files live in a private temporary directory removed when
`run` returns. Copy anything you need to keep. The URL and actor let the child call Metronome as the same
actor against the same service.

The wrapper renews every `--every` interval. A conflict stops renewal but does not kill the command. Other
renewal errors are reported and retried. The agent must stop work if it learns its run has closed.

When the command exits, renewal stops before the wrapper closes the run:

| Outcome | What `run` does | Exit code |
| --- | --- | --- |
| Invalid usage | No claim. | 2 |
| Claim fails | No command starts. | 1 |
| Brief cannot be read or command cannot start | Drops the claimed run, best effort. | 1 |
| Command exits 0 with a branch | Submits on that branch. | 0 |
| Command exits 0 without a branch | Drops the run. | 0 |
| Command exits non-zero | Drops the run, even if a branch was written. | Command's code |
| Command is killed by a signal | Drops the run. | 128 + signal number |
| Run already closed (for example, the agent asked) | Leaves the run alone and reports the conflict. | Command's code |

The branch file must contain a single branch name. Surrounding whitespace is trimmed. A missing, empty or
multi-line file means no branch. A failure to close the run other than a conflict changes a successful
command's exit code to 1. Read the messages as well as the exit code: exit 0 does not guarantee submission.

`run` forwards SIGINT and SIGTERM to the command. Cancellation of its caller forwards SIGTERM. Ctrl-C in a
terminal may deliver SIGINT to the command twice; this is an open question in [DECISIONS.md](../DECISIONS.md).

For example, start a Claude Code agent in the slice's repository:

```sh
metronome run --runner laptop -- claude -p 'Read the JSON file at $METRONOME_BRIEF.
Follow the repository instructions and complete the slice. If blocked, use
metronome ask --run "$METRONOME_RUN_ID" --type answer --question "Your question"
with your actual question, then stop. Only write the branch name to
$METRONOME_BRANCH_FILE when the checked work is pushed or ready to ship to that branch.'
```

The person starting the agent must ensure the claim matches the workspace, for example by using `--slice`
when targeting a known slice. They must also arrange how the work reaches the branch. The wrapper does not
check out a repository or ship code.

## Run the agent somewhere else

For a sandbox or remote runner, run a wrapper script on the host under `metronome run`. The host holds the
claim and renews the lease. The script copies the brief into the other environment, runs the agent there,
ships the result to a branch in the slice's repository, then writes that branch to the host branch file.

For example, invoke your own script with `metronome run --runner laptop -- ./run-agent.sh`. The script follows
this outline (the comments stand for environment-specific steps, not Metronome commands):

```sh
# Copy "$METRONOME_BRIEF" into the agent's workspace.
# Run the agent there with that copy of the brief.
# If the agent asked a question, raise it on the host if needed, then stop.
# If the agent failed, exit non-zero.
# Ship the result to the agreed branch. Stop if shipping fails.
printf '%s\n' "metronome/$METRONOME_SLICE_ID" > "$METRONOME_BRANCH_FILE"
```

Write the actual shipped branch, and exit 0 only after success. A failed agent or failed ship must not result
in submission. Leave the branch file empty or exit non-zero to drop the run.

The default service listens on `127.0.0.1:7340`, which a separate sandbox usually cannot reach. If the agent
needs to ask, either arrange access to the service and pass `METRONOME_URL`, `METRONOME_ACTOR` and
`METRONOME_RUN_ID` into the sandbox, or have the agent leave its question in a file and stop. In the latter
case, the host script reads the question and calls `metronome ask --run "$METRONOME_RUN_ID" --type answer
--question "$question"` before exiting. It must not ship or write a branch after asking. These are wrapper
responsibilities, not built-in sandbox support.

## Rules for agents

- Do not work without an open claim from `next` or `run`.
- Do not keep working, report or submit after the run closes, whether by ask, submit, drop or lease expiry.
  Commands on a closed run fail with a conflict. Claim again before resuming work.
- Do not guess at a human decision. Ask, usually with `--type answer`, and stop.
- Do not answer your own asks, approve plans or PRs, or record GitHub events by hand. Agents propose and the
  tracker decides. Rules decide approvals. Authored instructions take precedence over generated material.
  Incomplete work is never clean.

## See also

- `metronome help` for commands and flags.
- [README.md](../README.md) for building and starting the service.
- [DECISIONS.md](../DECISIONS.md) for current choices and open questions.
- [SPEC.md](../SPEC.md) for the design and principles.
