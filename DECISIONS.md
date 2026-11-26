# Decisions and open questions

This file records what has been decided about Metronome, what the review of SPEC.md found, and what is still
open.

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

## Designed, not built: source workstreams (2026-11-26)

Recorded so the data model leaves room for it. Nothing here is in the proof of concept.

- **A workstream may have no capacity** (limits of 0) and only supply ranked work. Other workstreams subscribe
  to it with an allocation (at most N of its slices in flight) and an ownership filter (the paths, routes or
  repositories they own).
- **The source's ranking holds.** Within its filter, a subscriber takes items in the source's order. Taking one
  out of order is allowed, needs a reason, and shows in the source's view.
- **Staffing an initiative is a settings change:** the source gains limits and members, who take items in
  strict priority order. Subscriptions carry on alongside.
- **An item flows through the workstream that pulled it** and keeps its link to the source. The source's view
  is a query across every item linked to it.

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
  required only at tiers 2 and 3. (Finding 7, except merge order across repositories, which stays Open.)
- **Default:** a `comment` verdict means not approved, and the PR goes to a human per its tier. (Finding 4.)
- **Default for the proof of concept:** the backlog is a local file. Metronome reads items from it and writes the
  items it proposes to a separate file. The general backlog contract is still to be written. (Finding 5.)

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

## Verified on the development machine

- 2026-11-26: the `codex`, `copilot` and `agy` command-line tools are installed alongside Claude Code. Each one's
  hook, MCP and background-session support has not been checked yet.

## Open

Everything section 12 of SPEC.md marks Open or Unverified, plus:

- The repository for the proof-of-concept pilot, and the mapping from its approver's rules to tiers 0 to 3.
- How the local service authenticates to the approver's results.
- When a workstream's own backlog and its subscriptions compete for one free slot, which goes first.
- Which other kinds of identity link to support, such as agent attribution tools.
- How the OIDC provider is configured for a self-hosted install.
