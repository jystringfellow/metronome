# Decisions and open questions

This file records what has been decided about Metronome, what the review of SPEC.md found, and what is still
open.

## Decided (2026-11-27): backlog item labels

- Agent's decisions: accept optional source labels, trimming and collapsing whitespace, lowercasing,
  de-duplicating and sorting before comparison. Allow at most 20 distinct labels, each 1 to 40
  characters, starting with an ASCII letter or digit and containing only `a-z`, `0-9`, space and
  `. _ - : /`. Exclude commas to keep the text board unambiguous. Refuse invalid sources rather than
  dropping labels.
- Keep sync add-only. Intake updates labels only on Ready items and on restoration, with full replacement
  in `item_updated` and `item_restored`; absent labels mean none. Existing payloads replay without migration.
- Filter the board server-side with the normalised `label` query (CLI `board --label`), keeping matching
  items, their slices, open runs and open asks. Capacity is unchanged. The read-only page shows labels.
- Always include `item.labels` in JSON briefs, using `[]` when empty.
- Agent's testing decision (2026-11-27): test the served page and automatic label refresh only when
  `METRONOME_BROWSER` explicitly names a Chromium executable. CI skips this test by default; a skip does
  not verify rendering.

## Decided (2026-11-27): what the brief tells a resuming runner

- Maintainer's decision: add rework status and reason, the previous branch and PR, and answered asks to the
  brief, additively.
- Agent's decisions:
  - Add a `history` section with `reason`, `rework`, `rework_reason`, `failed_head`, `changes_requested_by`,
    `previous_run`, `branch`, `pr` and `asks`, leaving existing brief fields unchanged.
  - Use resume reasons `checks_failed`, `changes_requested`, `lease_expired`, `dropped`, `answered_ask` and
    `ask_timed_out` (empty for fresh work). Include `ask_timed_out` so a runner can distinguish a timeout
    from an answer. Keep the rework reason and associated evidence until the next submit, so interruption
    does not obscure the original reason for rework.
  - Use `null` for an absent PR and `[]` for no answered asks. List only resolved slice asks, in raised
    order, because a timeout is not a decision and an item ask is not specific to the slice. Take the
    answerer and answer time from the `flag_resolved` event envelope.
  - Derive state from existing events so old logs replay without migration. Snapshot history when the
    claim is applied, so a repeated request for the same run reads the same history even after submit.
  - Read that snapshot via `GET /v1/runs/{id}/history`. This extends the earlier choice of no new endpoint
    for the slice brief: the board omits closed asks, and live slice history can change after a claim.

## Decided (2026-11-27): the board page

- Maintainer's decision: on 2026-11-27 the maintainer asked for a way to watch work being picked up and
  worked on. `metronome serve` now serves a read-only board page at `/`, the first version of the proposed
  web view, brought forward ahead of the reviewer and intake adapters.
- Agent's decisions:
  - Read only `GET /v1/board` and `GET /v1/events`, because they already carry the required data. The first
    load reads the whole event log in pages to find the latest 30; subsequent reads are incremental. Verify
    the last event's ID and sequence before continuing, because a replaced database can otherwise leave a
    stale event history even when an incremental query returns no events.
  - Refresh every five seconds and pause when the tab is hidden, because leases and polling move on minute
    scales and the service is local.
  - Hide done and dropped items and merged slices, with counts, to match `metronome board`.
  - Compute lease time left against the service's clock using the HTTP `Date` header, so skewed viewer
    clocks do not mislead.
  - Embed separate scripts and styles and use a strict Content-Security-Policy (`default-src 'none'`,
    `form-action 'none'`) to let the browser enforce the read-only, self-contained view.
  - Link PRs only for validated `OWNER/NAME` repositories and positive PR numbers, so other repository
    identifiers remain plain text rather than misleading links.

## Agent decision (2026-11-28): board page with workstreams first-class

- Read the unfiltered board and scope items, slices, runs and asks in the page, mirroring the server's
  label filter without filtering capacity. Keep one label at a time because multiple-label OR is Open.
  Switch scope with address links and `pushState`, without reloading; browser history restores scope.
- Add `lease_seconds` to the board contract. Mark leases expiring within the greater of 60 seconds and
  20% of the configured lease, and distinguish expired open runs from stalled slices.
- Put all workstreams' live lanes before any backlog in All view, then group the backlog by workstream.
  In a single-workstream view, keep the backlog inside its lane after live work. Show five Ready rows per
  workstream with the remainder and shelved items in collapsed details. Keep global per-approver usage
  separate from workstream capacity.
- Buffer the latest 300 events and show up to 30 matching sentences; explicitly count recent events that
  could not be placed in the filtered view. Highlight new or changed entities and events until the next
  successful refresh; preserve keyboard focus and expanded details across refreshes.
- Keep links and details as the only controls, and keep the original read-only CSP and embedded assets.

## Decided (2026-11-27): runner instructions

- [docs/runners.md](docs/runners.md) is the guide for agents taking work from Metronome and people wiring
  them up. Keep it in sync with the CLI.

## Decided (2026-11-27): metronome run

- `run --runner ID [--slice S] [--every D] -- CMD [ARGS...]` claims exactly as `next` does. The global request
  ID applies only to the claim. If the brief cannot be read or the command cannot start, the wrapper drops the
  run best effort. Metronome's own messages go to stderr; the command keeps stdin, stdout and stderr.
- The command inherits its environment with these overrides (changes to this contract are additive only):
  - `METRONOME_RUN_ID`: the claimed run ID.
  - `METRONOME_SLICE_ID`: the claimed slice ID.
  - `METRONOME_RUNNER`: the runner ID from `--runner`.
  - `METRONOME_BRIEF`: absolute path to a file containing the same bytes as `next --json` for the claim.
  - `METRONOME_BRANCH_FILE`: absolute path to an initially empty file for the command to write its branch name.
  - `METRONOME_URL`: the service URL in effect.
  - `METRONOME_ACTOR`: the actor in effect.
  Both files live in a private temporary directory removed when `run` returns.
- Renew every five minutes by default, leaving margin on the 15-minute lease. A conflict stops renewal without
  killing the command; other renewal errors are reported and retried. Renewal stops before closing the run.
- On command exit 0 with a branch, submit; otherwise drop, keeping the slice's slots. The branch is the file's
  content with surrounding whitespace trimmed; missing, empty or multi-line content means no branch. If the run
  is already closed, do not close it again. A conflict on closing is reported, including the intended branch
  when submitting.
- Always forward SIGTERM to the command and forward SIGINT only when stdin is not a terminal; cancellation
  of the caller forwards SIGTERM. Exit with the command's
  code (128 plus the signal number if killed by a signal), except that failure to close the run other than a
  conflict changes a successful command's exit code to 1. A submit lost to lease expiry also exits 1,
  as described in the closing retry decision below. Invalid usage exits 2; claiming or starting the
  command failing exits 1.

## Agent decision (2026-11-27): metronome run retries closing its run

- After the command exits, retry submit or drop on transport errors and HTTP 500 or above, with a 10-second
  timeout per attempt and exponential backoff from one second to at most 30 seconds. Other HTTP errors,
  including conflict, are not retried. Best-effort drops before command start remain single attempts.
- Remember the latest lease deadline returned by a heartbeat without changing the renewal schedule or
  conflict behaviour. Stop retrying one minute past the last known deadline to allow for clock skew, or
  after 30 minutes from the first close attempt to bound retries even with a wrong clock or long lease.
  The grace exceeds the maximum delay so there is an opportunity to learn about expiry from the service.
- All close attempts use `metronome-run-<runID>-close` as their request ID; a committed close whose response
  was lost is then recognised as a duplicate success. The global request ID still applies only to the claim.
- Add `api.StatusError`, wrapping the original error without changing its text or flow error code, so the
  CLI can distinguish server errors from permanent HTTP failures. Add read-only `GET /v1/runs/{id}` to
  retrieve a run even when closed, without ticking, extending the earlier choice to read only the board and
  run history. Use it once after a closing conflict to distinguish lease expiry from another closure.
- A submit lost to lease expiry, whether confirmed by a conflict or inferred when retries give up, reports
  the deadline, slice and branch to resubmit and changes a successful command's exit code to 1. A drop
  overtaken by lease expiry keeps the conflict rule (the command's code). Giving up without an answer is
  a failure to close, so exit 1 for a successful command or preserve a non-zero command code. Other
  conflicts retain the existing exit-code rule.
- SIGINT and SIGTERM received while closing stop retries as well as in-flight close attempts. Discard a
  signal still buffered from the command's exit before watching for closing interrupts, so the run can
  still close after the command is interrupted. Caller cancellation still does not prevent closing the
  run after the command exits.

## Decided (2026-11-27): each interrupt reaches the command once

- Decided by the maintainer: keep the command and its own subprocesses in the wrapper's process group, so
  terminal Ctrl-C reaches the whole tree once. Do not create process groups, change the terminal's foreground
  process group or use raw system calls. The separate-process-group approach was tried and rejected as too
  complex and untested on macOS.
- Suppress SIGINT forwarding when stdin is a terminal, as checked by the existing `go-isatty` dependency
  (`/dev/null` is not a terminal). Keep catching SIGINT so the wrapper can close the run when the command
  exits. Always forward SIGTERM, and forward SIGINT when stdin is not a terminal.
- Limitation: with a terminal on stdin, SIGINT sent only to the wrapper's PID (for example `kill -INT`) is
  not forwarded. Process managers and automation send SIGTERM, which is forwarded.

## Decided (2026-11-27): the slice brief

- `metronome next` prints the claim, slice and item. With `--json`, it prints one object with these sections:
  `claim` (`workstream`, `run_id`, `slice_id`, `lease_until`; `workstream` added 2026-11-27), `slice` (`id`, `repo`, `title`, `criteria`, `approvers`),
  and `item` (`id`, `ref`, `title`, `intent`, `labels`; `labels` added 2026-11-27). Every field is always
  present; empty criteria, approvers and labels are arrays, absent intent is an empty string, and the
  lease is an RFC 3339 timestamp in UTC. Changes to this
  contract are additive only. `metronome run` will hand this same object to its command.
- The CLI reads `GET /v1/board` after claiming. No new endpoint is needed because the slice and item brief
  fields are fixed once written. If the read fails or the slice or item is missing, the CLI reports the run
  and fails without printing a partial brief.
- The brief is not the context bundle (`get_bundle`) in SPEC.md section 9, which stays as designed and is
  still to be built.

## Decided (2026-11-26)

- **Local first, hosted as the goal.** The local version proves the protocol with one person and one repository.
  WIP limits are per workstream and work is shared across people, so the real thing needs one shared, hosted
  record.
- **Its own repository.** Metronome is not part of kapellmeister or scoring-stage.
- **It does not own the backlog.** Teams keep their backlog wherever they like: a tracker, an issue list or a
  file. Mirroring status back into it is the team's own skill or plugin. Metronome cares only about the shape of
  what the backlog hands it.
- **The proof of concept is build steps 1 and 2:** the tracker and claim protocol, then the review contract
  wrapped around a rules-gated approver in shadow mode.
- **Public and generic.** Organisation-specific adapters and notes live in a separate private repository. See
  AGENTS.md.
- **Go,** to match kapellmeister and scoring-stage. One binary serves the API, the CLI and the MCP server.
- **One HTTP API from the first day.** The CLI and the MCP server both call it, even when it is a process on a
  laptop.
- **An append-only event log is the source of truth.** Slots, leases and item state are derived from it. That
  makes events idempotent and replayable, lets SQLite be swapped for Postgres, and makes review cycle time and
  blocked-item age queries over the log.
- **GitHub events sit behind one interface:** polling `gh` locally and webhooks when hosted, with the same event
  shape.

## Decided (2026-11-26): workstreams, identity and execution

- **WIP limits are set per workstream.** A workstream is a board with a backlog and stage limits: one team's
  board, or an initiative that spans teams. It replaces the team as the scope of limits in SPEC.md section 3.
- **Two limits protect one person across every workstream they touch:** the per-approver cap, and the local
  runner cap (how many claims one person's runners may hold). They are ceilings, not capacity, and never decide
  what a workstream takes on.
- **No directory groups.** A workstream's settings list its members and their roles (owner or member). Owners
  change its limits and settings.
- **Who is accountable for automatic cloud dispatch is a workstream setting:** a named service identity or the
  item's owner. A slice can be moved from cloud to local and back by command. A move hands over the lease and
  keeps the slice's slots, so it never frees or takes capacity.
- **Execution starts local only.** Runners on people's machines claim work through the CLI, the MCP server or
  the API, from one shared, hosted record. Authentication for cloud runners is deferred. The preferred design is
  a cloud runner platform that issues tokens with Metronome as their audience, bound to the run.
- **People sign in with any standard OIDC provider,** by device flow or browser sign-in with PKCE. Metronome
  records whom it acts for and never acts as anyone: each event carries the accountable person, the actor (the
  runner and its lease) and where the credential came from. On claim, Metronome issues its own short token
  scoped to the claimed slice, which also fences stale writes.
- **Identity links are optional and made by the person.** `metronome login` offers to link a GitHub account
  through GitHub's OAuth device flow. A link ties an outside author ID to a person and is used only to attribute
  work. A missing link never blocks anything, and approver caps count GitHub logins directly.
- **Names:** workstream; item, a backlog entry; slice, the claimable unit, usually one PR; run, one claim
  attempt; thread, the conversation on a slice. "Task" is avoided because other tools use it for other things.
- **Every item carries its workstream and an optional source link from the start,** so pulling work between
  workstreams can be added later without migrating data.

## Decided (2026-11-26): a merge before approval

- **A merge counts as approval by whoever merged.** When a tracked PR merges while its slice is in `pr_open` or
  `in_review`, Metronome records the slice as ready for review if it was not yet, approved at the merged head by
  the user GitHub names as the merger, and then merged. Review and Release slots move exactly as for an approval
  through review. Why: GitHub never lets a PR's author approve it, so a maintainer whose agents open PRs under
  their own account, or a team that merges with admin rights, would otherwise leave slices holding Review slots
  forever. Merging is already a deliberate human act, which section 1's "rules decide approvals" allows as the
  rule.
- **Not recorded:** a merge while the slice is being reworked (queued, running, flagged or stalled), and a merge
  GitHub reports no merger for. The poller says so instead.
- Proposed by the agent and accepted by the maintainer on 2026-11-26.

## Decided (2026-11-26): failed checks in review

- **A red head sends a slice in review back to the fixer.** Failed checks on the PR's current head of an
  `in_review` slice stall it as rework, exactly as on `pr_open`: the next claim takes it first, it keeps its
  Review reservation, and the claim takes an Execute slot again, as rework after a change request does. It
  leaves its approver's count until it returns to review on green checks. Why: a red head cannot be approved,
  so leaving it in review held Review capacity and the approver's attention while nobody could fix it.
- **Failed check results for any other head are refused as stale** and change nothing.
- Proposed by the agent and accepted by the maintainer on 2026-11-26.

## Decided (2026-11-27): current-head checks

- **Green check results for any other head are also refused as stale.** A new head must be recorded with
  `pr-opened` before recording checks on it, including when checks are entered by hand. Why: a stale green
  result would move the recorded head back and let `ReadyForReview` trust an old commit.
- Proposed by the implementing agent and accepted by the maintainer on 2026-11-27.

## Decided (2026-11-27): a head that moves after approval

- A new head recorded for an `approved` slice withdraws the approval. The slice goes back to `pr_open` on the
  new head, with its approval and checks cleared. It needs green checks and a fresh approval, or a merge, which
  counts as approval by whoever merged. The poller records the moved head; a merge at a head other than the
  approved one records the new head first, then the merge. Approvals are per head SHA, so unreviewed commits
  must not ride an old approval.
- Decided by the maintainer on 2026-11-27.

## Agent decision (2026-11-27): withdrawing an approval

- Restore the Review reservation even over the Review limit. New claims needing a reservation wait until usage
  drops below the limit. The moved head is a fact: refusing it would preserve a stale approval, and leaving it
  uncounted would hide reviewer load.
- Return a `releasing` or `awaiting_release` item to `executing`, because it no longer has every approval. Free
  its held Release slot and hand it to the best-ranked item awaiting release, just as `release-done` does.
- Approved or merged siblings hold no Review slot while waiting on an unapproved slice, so move the awaiting
  item's reservation to the withdrawn slice if a sibling kept it, even if that sibling has already merged.
  Only the withdrawn slice needs review again; keeping both reservations would count it twice.
- Do not take an Execute slot, because no runner is working on the new head.
- Refuse `pr-opened` at the approved head, so a repeat cannot erase an approval or its checks.
- Report another open PR on the branch at the approved head without recording it, since no new head withdraws
  the approval.
- Report, but do not record, a merge at a moved head with no merger named, following the 2026-11-26 rule that
  approval by merge needs a named merger.
- Decided by the implementing agent on 2026-11-27, separate from the maintainer's decision.

## Decided (2026-11-27): tier 0 and 1 items are done at their last merge

- Decided by the maintainer on 2026-11-27: items whose confirmed tier is 0 or 1 never take a Release slot or
  await release. Merging ends Review, and the item is done when its last slice merges, without `release-done`.
  UAT is required only at tiers 2 and 3; low-risk items had nothing to release but held one of two Release slots,
  backing up Review. Tiers 2 and 3 keep the existing release path.

## Agent decision (2026-11-27): finishing low-tier items

- The confirmed tier from plan approval is stored as `confirmed_tier` using the existing `plan_approved` event.
  Only the confirmed tier decides whether Release is needed, because effective tiers are not recorded yet.
- The last approved slice keeps Review until its own merge, so the wait for merging is counted. Siblings
  approved earlier free Review as before. Corrected by the implementing agent on 2026-11-27 after live testing:
  keeping Review until the item's last merge contradicted the maintainer's rule that merging ends Review.
- New merges of executing low-tier items record `frees_review` in `pr_merged`, so Review is freed even when
  another slice has not merged. Historical events without that field retain their original meaning, and tier
  2 and 3 or legacy release-path merges continue to use the original payload.
- The item stays executing until a new `item_done` event at its last merge frees every slot its slices hold.
  Existing events are not reinterpreted, so replay preserves historical release state.
- A low-tier item already releasing or awaiting release in an existing log follows the old path and finishes
  with `release-done`. If its approval is withdrawn and it returns to executing, it follows the new rule from
  then on.
- This decision supersedes the Release slot requirement for tiers 0 and 1 in SPEC.md sections 2.5 and 4 until
  the maintainer updates the SPEC.

## Decided (2026-11-27): merges and closes during rework

- Decided by the maintainer on 2026-11-27: the poller follows queued, running and flagged slices with a recorded
  PR, as it does stalled slices, only to report a merge or close. The note names the slice, the PR, what happened,
  who merged if known, and that a person must decide. Nothing is recorded, and an open PR on such a slice
  records nothing until the fixer resubmits. Merged slices and rework slices without a recorded PR are not
  followed; tracking of submitted slices and slices waiting for review or merge is unchanged.

## Agent decision (2026-11-27): reporting during rework

- The note names the slice's status so a person can tell a running fixer from a waiting one. Stalled notes
  keep their wording.
- A parked slice is queued, so it is followed too if it has a PR, consistent with the maintainer's decision.
- An open PR on these slices produces no note, as for stalled slices, to keep polls quiet while a fixer works.

## Decided by the maintainer (2026-11-28): a slice fixed outside a run returns to review

- A slice waiting for rework (queued or stalled, not claimed by a run) returns to review when its recorded
  PR gets a new green head. Record the pusher; no runner is needed. This avoids the stuck item seen in #48.
- A subsequent merge is recorded as for any slice in review, counting as approval by whoever merged it.
- Running rework is unaffected: the runner's push and submit remain the path.
- A new red head or one without completed checks leaves the slice in rework.
- Cover these rules in reconcile cases and end-to-end poll tests. This narrows the 2026-11-26 rule against
  recording merges during rework and the 2026-11-27 rule that open PRs wait for resubmit, for queued and
  stalled slices only.

## Agent decision (2026-11-28): implementing fixes outside a run

- Compose a new `rework_fixed` event after `pr_opened` and green `checks_completed` in one command, so
  existing event logs retain their meaning.
- Use the head commit's first linked author login as the pusher because `gh pr list` does not expose the
  push actor. Record the fix even if the login is missing, with a note rather than inventing a name.
- Exclude flagged slices because an open ask awaits a person; only queued and stalled slices qualify.
- Only the recorded PR counts, since a replacement PR has not been associated with the slice.
- Draft PRs stay in rework because drafts do not move into review.
- Restore the Review reservation even over its limit, as for approval withdrawal; do not take an Execute
  slot because no runner is working.
- Clear rework evidence as at submit because the slice is no longer waiting for a fix.
- A merge first seen at a new green head with a known merger records the fix then the merge, as if a poll
  had observed the push before the merge.
- Note a new head with no checks so a person knows why the slice stays in rework. Other open-PR rework
  polls stay quiet unless recording a fix or evaluating its review.
- A claim racing with the poll surfaces as a poll error rather than displacing the run.

## Designed, not built: open workstreams and shared work (2026-11-26)

Recorded so the data model leaves room for it. Nothing here is in the proof of concept.

- **Anyone can create a workstream.** Its intake is pluggable: a skill, script or adapter fills its backlog and
  ranks it, for example by usage or performance data.
- **People find workstreams and choose how to give them time.** They participate by joining one as a member and
  claiming from it directly, or they pull an item from it into their own workstream by reference.
- **A pull by reference moves the work, not the record.** The item counts against the limits of the workstream
  that pulled it, and keeps its link to the source, so the source's ranking and reporting still see it. An item
  is in one workstream at a time, and the source shows where it went.
- **A workstream may have no capacity** (limits of 0) and only supply ranked work. Other workstreams subscribe
  to it, or pull from it by reference, using an ownership filter (the paths, routes or repositories they own).
- **The source's ranking holds.** Within its filter, a subscriber takes items in the source's order. Taking one
  out of order is allowed, needs a reason, and shows in the source's view.
- **A subscription is a standing pull:** an allocation (at most N slices in flight) plus an ownership filter, so a
  team keeps drawing from a source without pulling each item by hand.
- **Staffing an initiative is a settings change:** the source gains limits and members, who take items in
  strict priority order. Subscriptions carry on alongside.
- **An item flows through the workstream that pulled it** and keeps its link to the source. The source's view
  is a query across every item linked to it.

## Decided by the maintainer (2026-11-27): workstreams, minimal first cut

- Each workstream has a name and five stage limits, defaulting to today's config. The per-approver cap stays
  global per person. Zero capacity is allowed. Anyone may create a workstream; create, set and list are CLI
  commands. Items belong to exactly one workstream; ranks are local to it. Sync, plan start, next and board
  accept a workstream filter. Existing logs replay into `default`. Pull by reference and subscriptions remain
  designed, not built, following the decisions of 2026-11-26.

## Agent decision (2026-11-27): implementing workstreams

- Across workstreams, priority classes (rework, held slots, front) win first, then position in each workstream's
  own priority order, then oldest item, workstream name and ID. Planning uses front, position and the same
  tie-breakers. This avoids comparing unrelated source ranks, while keeping the old single-stream order.
- Names match `^[a-z0-9][a-z0-9._-]{0,63}$` for URL path compatibility. Creation and limit edits record all five
  resolved limits to keep replay independent of later defaults. `default` follows config until explicitly set.
- Ready zero adds nothing; the global refill threshold is compared against each workstream's Ready usage.
  This preserves the current refill policy. Refs remain globally unique and known refs are skipped, since moving
  items by reference is not implemented.
- Lowering limits retains held slots; no new slot is allocated while usage meets or exceeds the limit. Release
  increases fill waiting items immediately, and a freed Release slot goes to the same workstream only, so
  release capacity is neither stranded nor transferred to another board.
- The board's top-level limits remain config defaults and usage remains aggregate, for existing consumers.
  Workstream views carry their own limits and usage. The claim event and claim result name the workstream for
  auditing and the future brief. No source link field is needed yet; absence means none and a later field is
  additive.

## Open (2026-11-27): workstream follow-up

- Who may change limits once membership and ownership are built? For now anyone can, with the actor recorded.
- Should sync report refs skipped because they already belong to a different workstream?
- Should the Ready refill threshold become per workstream?
- This branch lacks the main branch's claim brief, `metronome run`, runners guide and web board. On merge,
  carry `claim.workstream` from `flow.Claim` into the brief; carry `--workstream` into `next` and `run` and the
  runners guide (`docs/runners.md`); group the web board by workstream.
  Also add `slice.dependencies: [{"slice_id":"I1.1","title":"schema","status":"merged"}]` to the brief,
  resolving `after` against the board slices in listed order, and show `after: I1.1 (merged)` in the text
  brief. Test dependency reporting by `next --json` and claim gating by `run` on merge. In `docs/runners.md`,
  explain that a slice with unmerged dependencies is not offered; `next --slice` on one fails with a
  conflict naming what it waits on; `no_work` may list waiting slices. Show `waits_on` on the web board.
  Link `docs/backlog.md` from `docs/runners.md` and list it beside the brief and the run environment as an
  additive-only contract.

- Agent update (2026-11-28): the web board is now grouped by workstream and shows `waits_on`.

## Decided by the maintainer (2026-11-27): pre-planned slices in the backlog

- Backlog items may carry `slices` with the fields accepted by `plan propose`. On entering Plan these
  become the proposed plan, and the owner is asked to approve it. Items without slices behave as before.
- `predicted_tier` is optional; when absent, the item is tier 1 and the approval asks to confirm it.
  Previously a missing tier silently became 0. Present tiers retain their validation.
- The backlog contract is documented in `docs/backlog.md` and is additive only. Existing logs replay unchanged.

## Agent decision (2026-11-27): implementing pre-planned slices

- Store optional `slices` and `tier_defaulted` on `item_added`, with the resolved tier. Emit an explicit
  `plan_proposed` and approval ask on plan start, without changing `plan_started` replay semantics.
- The item's owner is the plan approver; the source cannot supply a different plan approver because the
  maintainer specified owner approval. Empty slices mean no plan, since a proposal needs at least one slice.
- Validate all items, including known refs, before refill, matching tier validation. A known ref is skipped
  entirely, including its slices and tier, since updating an authored plan needs a separate rule.
- Parking clears the proposal but not the backlog slices, so re-entering Plan proposes them again. A hand
  proposal supersedes the current proposal, preserving the existing plan replacement behaviour.
- Keep the approval question's prefix and add sentences identifying a defaulted tier and backlog-supplied
  slices, so the owner understands what is being confirmed.
- Link the runners guide on merge: this branch lacks `docs/runners.md`, and a stub would conflict with main.

## Open (2026-11-27): pre-planned slices follow-up

- Should a re-sync update slices or tier of a known item that is still Ready?
- Should a source be able to name the plan approver?
- Should the text board show that a tier was defaulted?
- Should slices be able to depend on another item's slices, as in a migration plan? Cross-item dependencies
  remain open.

## Decided by the maintainer (2026-11-27): intake runs a source command

- The source owns the backlog. `metronome intake --workstream NAME [--every D] -- CMD` reads backlog JSON
  from CMD's stdout, once or periodically, without holding source credentials. Stderr passes through;
  a failed command or invalid result syncs nothing, reports the error and does not stop a periodic loop.
- Intake mirrors the source: add within Ready limits, rerank and update Ready titles, intent and labels
  (labels added 2026-11-27), and remove missing items from Ready. Work in Plan or later is never automatically
  dropped; report a disappearance.
  Report and skip claims held by other instances, local or hosted. Repeating the same sync is idempotent.

## Agent decision (2026-11-27): implementing intake

- Shelve rather than drop missing Ready items, preserving their identity for later restoration. The existing
  `item_parked` event returns an item to the front of Ready, so shelving has its own status and additive
  events. Hide shelves from the text board, but retain them in JSON and replay. The stored reason and claimant
  describe the shelving event, not a continuously updated source observation.
- Shelve before refill so newly freed room is usable immediately. Add and restore in source order under
  the existing refill threshold and Ready limit. Never evict an existing Ready item merely to make room
  for a higher-ranked new one; retain front priority for parked items.
- Update only rank, title, intent and labels on Ready items, including on restoration (labels added
  2026-11-27). Report changes to owner, value, tier or slices instead of deciding the open question about
  changing those fields.
- A foreign claim shelves a Ready item and prevents adding or restoring it. A foreign claim or disappearance
  in Plan or later produces a note, leaving authored work intact. Done and dropped items need no note.
  Report cross-workstream refs rather than moving them. Add-only `backlog sync` also skips foreign claims.
  In add-only sync, a reported foreign claim takes precedence over duplicate unmarked entries for that ref.
- Require a workstream and a non-null `items` array for intake, and reject repeated refs: there must be a
  single unambiguous, complete ranked list before removing work. Preserve add-only sync's existing defaults.
- Pass `METRONOME_WORKSTREAM` and the temporary `METRONOME_ITEMS` snapshot to the command so it can publish
  and recognise its own claims with its own credentials. `claimed_by` identifies someone else's claim.
  Metronome never writes to the source, preserving the decision that status mirroring is a team's skill or plugin.
- Refuse `--request-id` on intake because reusing one ID would suppress later rounds. Return informational
  notes in optional `Result.notes` to make skips and conflicts visible even when there are no events.

## Open (2026-11-27): intake follow-up

- Two instances can take an unmarked item before either source command publishes a claim. Should Metronome
  detect this or write the marker itself?
- Should a higher-ranked new item displace a lower-ranked Ready one?
- Should intake update owner, value, tier or slices of Ready items?
- Should shelved items expire?
- Should an intake command have a timeout?
- On merge with main, link `docs/intake.md` from `docs/runners.md` and list it alongside `docs/backlog.md`
  as an additive-only contract; also point to the built-in GitHub Issues source. This branch lacks the
  runners guide; do not create a conflicting stub.

## Decided by the maintainer (2026-11-27): a GitHub Issues intake command

- `github issues --repo OWNER/NAME [--label L] [--project OWNER/N --rank-field Rank] [--owner WHO]`
  reads open labelled issues via `gh`, default label `metronome`, and prints the backlog contract.
- Order by numeric project rank with unranked last, or oldest first without a project.
- Ref is `OWNER/NAME#N` unless a `Metronome: REF` line preserves the existing reference. Map title, body intent,
  first assignee or fallback owner, and optional `tier:N` label; supply one pre-planned slice in the issue's
  repository, with criteria from a task list or Acceptance criteria section. Skip pull requests and non-issue project items.

## Agent decision (2026-11-27): implementing GitHub Issues intake

- Paginate REST issues and typed GraphQL number fields to avoid truncation and guessing at project field keys.
  Fail on missing or nonnumeric fields and incomplete reads. Default the rank field to `Rank`, require a project
  when specifying it, and reject comma-separated labels (the REST filter would mean AND).
- Sort ranks ascending to match Metronome, breaking ties by creation time then issue number.
- Use the first prose paragraph, collapsed and capped at 500 runes, for one-line intent. Ignore template comments
  and fenced code. Match the ref marker case-insensitively outside fences; prefer an acceptance section over
  task lists, join wrapped items and retain checked tasks so authored criteria survive import.
- Agent follow-up (2026-11-27, gate 1): strip only closed HTML comments and refuse backtick fence openings
  whose info string contains a backtick, so literal Markdown examples do not discard subsequent refs or criteria.
- Missing owners, invalid or conflicting tiers, invalid issue identity and duplicate refs fail the entire read
  with per-issue errors, rather than silently omitting work. Validate repository names to avoid malformed REST paths.
- The command adds no events, contacts no tracker, sets no value or claim marker, and leaves approvers empty
  (the existing default review rule applies). Existing approval gates remain unchanged.

## Open (2026-11-27): GitHub Issues intake follow-up

- Resolved below: an instance-specific label marks external claims; an assignee alone does not.
- Should approvers or multiple slices (such as sub-issues) come from the issue?
- Should value come from a project field?
- Should the label filter support multiple labels (OR)?

## Decided by the maintainer (2026-11-27): publish mirrors intake

Metronome hands workstream transitions to the source's own command, which updates the source's workflow.
This refines the November 26 decision that mirroring status back is the team's own skill or plugin: the
core still never writes to a source or holds its credentials. The GitHub publisher is one such command.

## Agent decision (2026-11-27): implementing publish

- Derive flat transition names from status differences while applying events, with separate ask transitions.
  Existing events keep their meaning; live and replayed transitions use the same derivation.
- A new `publisher_advanced` event stores a sequence cursor per workstream and publisher. Advance uses
  compare-and-set; batches have a bound but never split one event. Rounds drain batches and skip empty runs.
- Keep opaque publisher JSON beside the cursor, limited to 256 KiB. Save changed valid state even on command
  failure, without advancing its cursor. This lets the GitHub publisher preserve last-written Status across
  machines and partially failed batches without a new field, a hidden comment before a PR, or an unreliable
  inference from the GitHub user (who can also edit the board) or the current mapping.
- Claims use `metronome:claimed:INSTANCE` labels; each instance uses the same name on intake and publish.
  Assign on claim and remove only assignments the publisher made. One hidden PR marker per distinct PR
  prevents duplicate comments. Keep a completed-transition watermark with keys at its last sequence so
  terminal state can be pruned without replaying completed claim and Status writes after partial failure.
- Default mapping: planning and ready → Ready; claimed, submitted, pr_open and rework → In progress;
  in_review and approved → In review; done → Done. Latest mapped transition wins for multi-slice items.
  Adopt an already matching Status on first observation; otherwise only change a value matching the last-written
  value. Do not adopt a later human edit even if it matches a mapped target: doing so would give subsequent
  transitions permission to overwrite the edit. Report unresolved or ambiguous body references and skip;
  fail on configuration and gh errors.
- Review follow-up (2026-11-27): save changed publisher state after an interrupted command using a separate,
  five-second commit context. Only successful commands advance the cursor. Parking a slice already in rework
  publishes `parked`, not another `rework` transition. Keep publisher state out of the generic State JSON.

## Open (2026-11-27): publish follow-up

- How should a publisher recover if it changed an issue but Metronome was killed before committing its state,
  or the advance POST failed (service unavailable or five-second timeout)? On replay, the publisher can mistake
  its own Status for a human edit and stop updating it, or leave its assignee on the issue after completion.
  Should a person reconcile the issue, or should replay adopt a Status matching the transition's target?
- The race before a claim marker appears remains open. Concurrent publishers or a person editing during
  a GitHub write are not protected by a source-side atomic compare-and-set.
- Should a new publisher be able to start at the current end of the log?
- How should Status aggregate for multi-slice items?
- Should publish commands have a timeout, and should publisher state expire? Each state-saving cursor event
  stores the complete opaque state, so the log can grow by up to 256 KiB per such event.
- How should a publisher recover from a permanently inaccessible direct issue ref (deleted or transferred)?
  For now a failed direct issue read retries the round: a missing issue and insufficient permissions are not
  always distinguishable, and incomplete reads must not be acknowledged as success.
- On merge, link the publish contract from `docs/runners.md` and list it with the other additive-only contracts.

## Agent decision (2026-11-27): implementing Azure DevOps intake

- Map work item tags, other than `tier:N`, to the item's labels (added to the backlog contract on
  2026-11-27). Each tag is normalised with Metronome's label rule; a tag the rule refuses is skipped rather
  than failing the whole read, and at most 20 labels are kept, in tag order. (Merged with main by the
  maintainer's reviewer on 2026-11-27, replacing this branch's own labels field.)
- Parse Predecessor links and retain their reference mapping in the adapter. Report links inside and
  outside the result, except finished predecessors. The brief's dependency criterion is only partly met:
  cross-item dependencies remain open (#46), and `after` cannot express these links.
- Use `az devops invoke` to distinguish empty flat queries from link queries and read expanded relations.
  POST WIQL text or GET a saved query, then POST batches of up to 200 IDs. Require complete results;
  use private temporary files for request bodies and disable automatic organisation detection.
  **Unverified:** the `wiql` and `workitemsbatch` resource names and response shapes are based on the
  planned REST contract and recorded-shape fixtures, not a live Azure DevOps organisation.
- Default ready states are New, Approved, To Do and Proposed; a comma-separated override replaces them.
- Done, Closed and Removed are always finished, omitted with notes so local Ready copies are shelved.
- Any assignee is a foreign claim; otherwise non-ready unfinished states are claimed as `state STATE`.
  Claimed items keep their rank position in the result.
- Owners use uniqueName, then displayName, then the explicit fallback. Rank uses BacklogPriority, then
  StackRank, ascending, missing last and ties by ID. Refs are always AB#ID; tier comes from a tier tag,
  value is zero, and a Repo marker supplies a single pre-planned slice. Without one, Plan proposes slices.
  Convert HTML to blocks before decoding entities; use the first description paragraph for intent and
  acceptance list items or paragraphs for criteria. Keep approval gates unchanged.
- Agent follow-up (2026-11-27, gate 1): recognise only syntactically valid tag names and delimiters,
  rejecting unquoted opening angle brackets inside tags. Preserve literal or malformed angle brackets
  as text and split plain fields by line when no tag or closed comment is consumed, so comparison
  expressions and standalone Repo markers survive conversion.

## Open (2026-11-27): Azure DevOps intake follow-up

- Should Business Value supply value, or a migration marker preserve existing refs?
- How should the adapter publish claims, and should ready-state categories replace process state names?
- Carry predecessor dependencies once cross-item dependencies (#46) are supported.
- Verify the az invoke call and response shapes against a live organisation.

## Decided by the maintainer (2026-11-27): slices can depend on other slices

- A proposed slice may list `after`, 1-based positions of slices of the same item that must merge first.
  Refuse cycles, self references and out-of-range positions at proposal. Neither automatic nor explicit
  claims (`next`, `next --slice`, `run`) may claim a waiting slice; an explicit claim names what it waits on.
  The last dependency merge makes it claimable without further action. The board, board data and brief
  show dependencies; existing event logs replay unchanged.

## Agent decision (2026-11-27): implementing slice dependencies

- Store `after` in the existing `plan_proposed` specs, resolve positions to slice IDs at `plan_approved`,
  and derive `waits_on` by removing IDs on `pr_merged`. No new event is needed; old logs have no
  dependencies. The board exposes both `after` and `waits_on`. Refuse duplicate positions rather than
  silently normalise the authored plan.
- Explicit waiting claims return `conflict` with each unmerged dependency's status. When no claim is
  possible, `no_work` names waiting slices in the requested workstream (or all workstreams without a
  filter), so a runner can see why. Existing capacity errors keep precedence.
- An approved item or slice cannot be dropped by the current engine: `plan drop` is only for items still
  planning. Dropping a plan therefore cannot strand a recorded dependent slice. A closed PR, or one
  merged on GitHub but not recordable in Metronome (for example, during rework or with no named merger
  when one is required), can still leave dependencies waiting. On each poll, report the waiting slices
  alongside the dependency's PR and say a person must decide. Record no new event and do not release
  dependencies on an unrecorded merge. This surfaces the blockage without treating incomplete work as clean.

## Open (2026-11-27): slice dependencies follow-up

- SPEC.md section 2.5 and finding 7 say an item's PRs merge together once all are approved. Ordered slices
  require an earlier PR to merge before the dependent slice can be claimed. The engine already accepts
  a merge of an approved slice before its siblings are approved, so ordered slices of a tier 2 or 3 item
  merge one by one ahead of Release. Is that intended, or should dependent tiers be separate items?
- Should dependencies be allowed across items, such as migration tiers planned as separate items?
- Should there be a recorded slice drop or PR abandonment event? Should dependent slices be dropped,
  released from the dependency, or raise an ask for the item owner?

## Proposed (2026-11-26), not decided

- **Release and Monitor read signal adapters.** An adapter records evidence on an item from a feature flag
  service (on or off, rollout percentage, targeting), traffic data (for example, the share of sessions on a new
  page against the old one) or an observability service (errors, latency, page load time). Each piece of
  evidence names its source and when it was read. Metronome keeps snapshots and a link, and the time series stay
  in the service that owns them. Uses:
  - Release progress and gates, such as "done when the flag is at 100% and the new page carries 90% of traffic".
  - Monitor checks, marking each hypothesis written at plan time proved or not from real numbers. A breach opens
    a triage item or uses the expedite slot.
  - Intake, where the same signals rank the backlog.
- **A read-only web view served by the Metronome binary,** built on the same HTTP API and event feed: the
  first version (one board, open runs, open asks and recent events) was built on 2026-11-27 at the maintainer's
  request. Per-workstream boards with stage columns and WIP counts, each person's inbox, an item page with
  its evidence timeline and panels for signal adapters stay proposed. Writes such as approving or answering
  an ask stay in the CLI and MCP server.

## Decided from the SPEC review (2026-11-26), now in SPEC.md

- **WIP defaults:** Review 8, counting every slice from claim to approval, and a per-approver cap of 2 PRs ready
  for review. The per-approver cap is the real throttle on reviewers; Execute 5 can now bind. (Finding 1.)
- **Three waiting states:** flagged keeps its slots; stalled (lease expired) keeps its slots and another runner
  can claim it; parked (an ask or a stall timed out) frees every slot. The stall timeout is Open. (Finding 2.)
- **Tier and mode are outputs of the approver,** reported for an exact head SHA. The adapter may start the review
  or observe one the approver ran on its own events. The tier is recomputed for every new head SHA and can only
  rise. An approver that cannot report a tier is advisory only. (Findings 3 and 6.)
- **Merging is the last step of Review.** When every PR of an item is approved, the item takes a Release slot and
  its PRs merge. If Release is full, the last approved PR keeps its Review slot, so the wait is counted. UAT is
  required only at tiers 2 and 3. (Finding 7, except merge order across repositories, which stays Open; for
  tiers 0 and 1, see the decision of 2026-11-27.)
- **Default:** a `comment` verdict means not approved, and the PR goes to a human per its tier. (Finding 4.)
- **Default for the proof of concept:** the backlog is a local file. Metronome reads items from it and writes the
  items it proposes to a separate file. The read-side contract is now in `docs/backlog.md` (2026-11-27);
  the write-side contract is still to be written. (Finding 5.)

## How the existing tools fit

- A runner is a scoring-stage sandbox playing a kapellmeister ensemble. `claim` starts the sandbox, the context
  bundle becomes the kapellmeister task, and `stage ship` with the Work-Item trailer delivers the branch.
- A kapellmeister result is a runner's report, never gate evidence.
- Neither tool records which work item a run was for. The claim protocol and the commit trailer join them.
- Nothing pushes from inside a sandbox; a person publishes with `stage ship`. So `submit` records what the runner
  intends, and the item moves only when the GitHub event arrives.

## Review of SPEC.md (2026-11-26)

The findings as raised. Findings 1–4, 6 and most of 7 are settled above; 5 is settled only for the proof of
concept; 8–11 are still proposals.

These affect the proof of concept:

1. **The WIP defaults no longer add up.** A claim holds a Review slot until approval, so at most 3 slices can be
   in flight and the Execute limit of 5 can never be reached. The Review limit now measures pipeline depth, and
   the per-approver cap (Open) is the real throttle on reviewers. Proposed: raise the Review pipeline default to
   6–8 and decide the per-approver cap.
2. **"Park" and lease expiry contradict each other.** Section 5 has a parked Plan item keep its slot; section 4
   has parking release the Review reservation; section 8 has `release` free the slot on park. Section 4 keeps the
   Execute slot on lease expiry; section 8 frees it. Proposed: three named states. *Flagged* keeps its slots.
   *Stalled* (lease expired) keeps its slots and another runner can take it. *Parked* (an ask timed out) frees
   everything.
3. **The reviewer contract assumes an approver it can call.** The rules-gated approver available for the pilot
   reviews on GitHub events, so nothing calls it. It takes its mode from the repository's rules file and chooses
   its own rule, which in effect computes the tier. Proposed: the adapter observes a review for
   `(repo, PR, head SHA)` and reports `effective_tier` and `mode` as outputs, never as inputs.
4. **`comment` has no transition.** Section 7 lists it as a verdict, but nothing says what it does to an item.
5. **The backlog contract is missing, and it must cover writing as well as reading.** Metronome creates backlog
   items in about six places: `/learn`, regenerating a stale doc, an incident after an auto-advanced change, a
   rollout breach, adopting an untracked PR, and a dropped plan. Proposed: the backlog hands over an item with
   intent, value score, owner, predicted tier and an external reference. Metronome proposes new items back
   through the same adapter. The backlog owns intent and rank; Metronome owns the ready queue and everything after
   it.
6. **The tier is computed only once, but the fixer and rework change the diff.** Proposed: recompute on every new
   head SHA, and only ever upward.

These can wait:

7. **Where merging happens is undefined.** The SPEC doesn't say whether merge belongs to Review or Release, or
   where an approved item waits when Release is full, which leaves a queue nobody counts. The Release exit gate
   requires UAT at every tier, while section 6 requires it only from Tier 2.
8. **Items with PRs at different tiers.** When a Tier 0 PR would auto-merge and its Tier 2 sibling needs a human
   merge, the SPEC doesn't say which rule applies. Proposed: the item's tier sets the merge mode for all its PRs.
9. **Sibling PRs in one repository.** Merging one leaves the other out of date. Updating its branch counts as a
   change, so it needs a fresh Review slot and a new approval. Dependencies between slices aren't modelled, and
   stacking is ruled out.
10. **Approved slices waiting to be claimed aren't counted anywhere.** It's also unstated when an item gives up its
    Plan slot, and "Plan pulls when Execute has room" compares items with slices.
11. **Smaller gaps.** "Change type", which earned trust depends on, is never defined. Fixer passes use runner
    compute without an Execute slot.

Wording: section 1's "one shared tracker" may read better as "one shared flow record", since Metronome owns flow
state (slots, leases, flags, gate evidence) and not the backlog.

## Proof-of-concept choices (2026-11-26), to confirm

Choices made while building step 1 where SPEC.md was silent. Each is easy to change.

- **Placeholder timings:** lease 15 minutes, stall timeout 2 hours, ask timeout 24 hours. All three are still
  Open in SPEC.md section 12.
- **Only Answer asks park on timeout.** SPEC.md section 5 has Plan approvals escalate, Review approvals reassign
  and Verify asks keep the item red. Escalation and reassignment are not built yet, so those asks stay open past
  their timeout.
- **An answered ask leaves a slice stalled:** it keeps its slots, and the next claim resumes it.
- **Abandoning a run (`drop`) leaves the slice stalled too,** per the decided waiting states.
- **Claim order within a workstream:** rework first, then slices already holding slots, then parked slices
  returned to the front, then new slices by backlog rank. See the 2026-11-27 workstream ordering rule for
  selection across workstreams.
- **Moving into review needs green checks on the PR's head.** The per-approver cap is shown on the board but not
  enforced, because a person can mark a PR ready on GitHub at any time.
- **An approval must name the PR's current head SHA,** or it is refused as stale. A moved head withdraws the
  approval, per the decision of 2026-11-27.
- **The expedite slot is not built yet.** Untracked PR adoption is implemented (2026-11-28).
- **The GitHub poller is level-triggered.** `metronome github poll` compares what GitHub shows now for each
  submitted slice's branch with what Metronome has recorded, and records only what differs, through the same API
  calls a person would make. Each slice carries a version that every event on it raises, and request IDs combine
  the GitHub fact with that version. A repeated poll records nothing, and a fact that comes back (a head pushed
  away and back, a check that fails and passes again) is still recorded. A webhook, when hosted, only needs to
  trigger the same comparison for one PR. It reads GitHub through the `gh` command, as whoever `gh` is signed in as.
- **What counts in review:** only reviews of the PR's current head count, each reviewer's latest review wins, a
  dismissed review counts as none, and a comment never approves. When a slice names approvers, only their reviews
  count; otherwise anyone's does. An approval also needs green checks on the head. A change request sends the
  slice back once; while it stands on the same head, the poller says it is waiting for that reviewer.
- **Checks:** only the latest run of each check on the head counts, and the worst of those decides. Skipped and
  neutral runs pass; cancelled runs fail. A head with no checks is never green, so a repository without CI keeps
  its slices out of review. A draft PR never moves into review.
- **Reported, not recorded:** a PR closed without merging, and a closed or merged PR on the branch that
  Metronome never recorded for the slice, such as one left from an earlier use of the branch name. The poller
  also follows a slice being reworked (queued, running, flagged or stalled) that has a recorded PR. Queued
  or stalled slices return to review on a new green head and may then merge. Running and flagged slices
  still wait for the fixer. Otherwise, when that PR is merged or closed, the poll names the slice, the PR,
  what happened and who merged if known, and says a person must decide. The engine refuses a merge while
  the slice remains in rework. (Updated 2026-11-28.)

## Verified on the development machine

- 2026-11-26: the `codex`, `copilot` and `agy` command-line tools are installed alongside Claude Code. Each one's
  hook, MCP and background-session support has not been checked yet.

## PR adoption (2026-11-28)

Maintainer decisions (2026-11-27 and slice criteria): adopt existing PRs once through a workstream label,
`github adopt`, or a source item naming a PR; new items belong to the adopting workstream and are owned
by the author. Normal review rules apply. Forks are refused (narrowing SPEC section 8's never-rejected rule).
Prefer the only non-draft open PR when a source names several, and report ambiguity. Rework runners update
the shared branch before working and ask if that cannot be done cleanly.

Agent decisions (2026-11-28):

- Record adoption in one additive `pr_adopted` event, using the existing Ready item ID for source adoption,
  because prior events require a run to record a branch. The slice holds Review at `pr_open`; green checks
  and a non-draft PR are still required to enter `in_review`. New work waits when Review is full.
- Skip plan approval because the work already exists; use the source tier or a PR's `tier:N` label (default 1).
  New items rank after existing work. Keep the source item's owner, recording PR authorship separately.
- Store the workstream adoption label with a separate event; opt into repository discovery with `--adopt-repo`
  rather than searching other people's repositories. Removing a label does not stop existing tracking.
- Use a per-item `prs` list so the poller can choose from current PR facts. Never plan Ready items naming PRs.
  Intake may update references only on Ready items, preserving work already in progress.
- Expose the adopted PR in `slice.adopted` and require the recorded branch on submit, because the service cannot
  inspect a runner's checkout. Follow the specific PR number rather than guessing from a reused branch name.
- Engine PR uniqueness plus request IDs make retrying safe. Reuse issue body parsing for PR criteria.
- Add an `adopted` publisher transition, mapped to In progress; set the claim marker without assigning the issue.
  Since adoption immediately tracks a PR, do not raise an extra untracked ask as described in SPEC section 8.
- Azure DevOps GitHub ArtifactLink connection-to-repository resolution is Unverified. Report the link and
  ask for a `PR:` line rather than guessing its repository.
- Agent decision (2026-11-28 review): keep unresolved linked-PR items out of fresh planning by marking them
  claimed when no explicit PR reference exists. Skip direct PR references in the issue publisher (including
  later item transitions); intake does not report direct adoptions missing from its source or newly named PRs
  already followed by the item. These observations do not require a human decision.

## Agent decision (2026-11-28): health probes and database backups

- Listen before replay. `/healthz` returns 200 `{"status":"ok"}` without touching the store, so a slow disk
  does not trigger a liveness restart. `/readyz` returns 503 `{"status":"starting"}` during replay and 200
  `{"status":"ready","last_seq":N}` after replay and a successful event table read. A failed read returns
  503 `{"status":"unavailable","error":"..."}`. Other routes return 503 until replay completes.
- The local CLI uses `VACUUM INTO` for a consistent snapshot without blocking WAL writers, verifies
  `integrity_check`, and syncs a temporary file before publishing with an atomic hard link. Unlike a rename,
  the link cannot overwrite a destination created by another backup concurrently. Restore is an offline
  file swap. There is no HTTP backup endpoint because the unauthenticated API would expose the whole log.
- No event or schema change; existing logs replay unchanged.

## Decided by the maintainer (2026-11-28): hosted shadow mode

One hosted instance runs `serve` with SQLite on persistent storage, intake, publish and GitHub polling beside
it, and an authenticating proxy in front of the board. Laptops do not write to it yet; OIDC and runner tokens
come later. Publish a generic image to GitHub's container registry and a generic Helm chart. Operators extend
the image with their own tools and maintain their own values and infrastructure elsewhere.

## Agent decision (2026-11-28): one configuration runs an instance

- Use strict JSON version 1, additive-only; unknown fields fail rather than silently changing behaviour.
- Apply declared workstream settings through the existing API. Omitted limits and labels stay unmanaged;
  nothing is deleted. No new events or replay semantics.
- Run existing intake, publish and poll rounds in-process through the service handler. Log each round by
  loop name; failures and panics retry with exponential backoff from one second capped at the interval.
- Inherit the operator's environment for secrets, with no secret fields or implicit shell expansion. Do not
  log command arguments or environment. Restart to apply changes; no reload in this slice.

## Open (2026-11-28): instance configuration follow-up

- Whether to accept YAML or reload on SIGHUP.
- Per-round command timeouts (see intake and publish follow-up); whether standalone loops should share backoff.
- Whether to pass `METRONOME_URL` to children, or have the file own `addr`, `db` and `tick`.
- Whether omitted limits should converge to defaults or undeclared workstreams be reported.
- Whether to support structured JSON logs for hosting.

## Open

Everything section 12 of SPEC.md marks Open or Unverified, plus:

- 2026-11-28: whether `serve` should schedule backups itself, and the retention and off-volume copy policy.
- 2026-11-28: whether to offer an authenticated backup endpoint once OIDC exists.
- 2026-11-28: whether readiness should change before shutdown to allow draining, or probes use a separate port.
- 2026-11-28: whether readiness should check freshness, such as the tick loop's last success.

- 2026-11-28: whether adoption should bypass full Review, whether label removal or PR closure should drop
  adopted items, whether forks allowing maintainer edits can be supported, how to resolve Azure DevOps linked
  PR repositories, whether intake should change PR references after Ready, and whether adopted items need
  an untracked ask.

- 2026-11-28: whether a flagged slice should also return to review on a green push.
- 2026-11-28: whether to use the push or timeline actor instead of the head commit author for the pusher.
- 2026-11-28: whether to adopt a replacement PR on a rework slice's branch.

- 2026-11-27: whether the closing retry schedule and bounds should become flags, and whether a lost submit
  should automatically raise an ask for the item owner.

- 2026-11-27: agent's open questions: whether labels should update on items in Plan or later, and whether
  to allow non-ASCII labels.
- 2026-11-27: what happens when an approver reports an effective tier of 2 or more on a tier 0 or 1 item.
  Effective tiers are not recorded yet, so only the confirmed tier decides today.
- 2026-11-27: whether plan approval should stop overwriting the predicted tier, which SPEC.md section 6 keeps
  for comparison. The existing board behaviour is preserved for now.
- 2026-11-27: what a withdrawal should do when a sibling PR of the item has already merged; today the item
  returns to executing and the merged PR stays merged.
- 2026-11-27: the board page has no authentication, like the API it reads; keep it on the default loopback
  address. How to protect the page and API when hosted with OIDC sign-in remains undecided.
- 2026-11-27: whether to add a tail query to the events feed once logs grow, to avoid reading the whole log
  on first load.
- 2026-11-27: whether to list item-level asks and timed-out asks in the runner brief.
- 2026-11-27: whether to record the head SHA a change request reviewed; this needs a new event payload field.
- The repository for the proof-of-concept pilot, and the mapping from its approver's rules to tiers 0 to 3.
- How the local service authenticates to the approver's results.
- When a workstream's own backlog and its subscriptions compete for one free slot, which goes first.
- Which other kinds of identity link to support, such as agent attribution tools.
- How the OIDC provider is configured for a self-hosted install.
