# SPEC.md: Metronome, an AI-driven work platform (full design)

This is the complete design for Metronome, a generic, shareable platform for AI-driven work tracking with built-in orchestration. It is written to be read by an AI agent. Your job has two parts. First, vet the design: list contradictions, gaps, and anything that cannot be built as described. Second, help build a proof-of-concept. Do not guess at anything marked Open or Unverified. Ask the human.

Status labels used below: Decided (agreed, change only after discussion), Default (a working number to validate), Open (undecided), Unverified (a claim not yet checked).

## 1. Goal and principles

One shared tracker where agents, local or cloud, pick up work and people set the pace. Skills and plugins cover each stage of delivery. An orchestrator hands out work within WIP limits so planning and execution never exceed what the team can review and release.

- Pull, never push.
- Agents propose and the tracker decides. State changes come from verifiable events.
- The AI produces findings. Deterministic rules owned by each repo decide approvals.
- Authored docs are normative and generated docs are descriptive. The resolver never lets a generated doc win a conflict.
- An incomplete result is never treated as clean.
- Humans are a flag and an inbox, not a stage.

## 2. The loop: seven stages

Backlog, Plan and refine, Execute, Test, Review, Release, Monitor, then back to the backlog. Each stage has skills, context, and an exit gate.

### 2.1 Backlog

- Purpose: discover, create, and prioritize work.
- Skills: /discover mines feedback, incidents, and metrics. /capture writes intent.md. /prioritize scores and ranks.
- Context: roadmap and strategy, support and sales signals, Monitor findings, past decisions.
- Exit gate: the item has intent, a value score, and an owner. A human sets the top of the ranked list.

### 2.2 Plan and refine

- Purpose: turn intent into executable pieces.
- Skills: /refine asks open questions and proposes a risk tier. /slice splits the item into one-session tasks. /spec writes acceptance criteria and a release hypothesis (what should change, measured how).
- Context: intent.md, AGENTS.md and architecture notes, dependency map.
- Exit gate: a human approves the plan and confirms the predicted tier. Slices are independent where possible.

### 2.3 Execute

- Purpose: runners, local or cloud, deliver each slice in isolation.
- Skills: /build plans first, then implements. /isolate gives each slice its own workspace. /handoff writes progress notes.
- Context: approved plan, AGENTS.md and repo skills, tool access and sandbox, work claimed through the claim protocol.
- Exit gate: runner self-checks pass. A draft PR links back to the work item and plan.

### 2.4 Test

- Purpose: earn confidence before a human looks.
- Skills: /verify runs unit, integration, and end-to-end tests. /evals checks AI behavior in CI. /confidence maps results to acceptance criteria.
- Context: acceptance criteria, test data and fixtures, staging environment.
- Exit gate: checks are green and the confidence report is attached to the PR.

### 2.5 Review

- Purpose: the same AI review passes on every PR.
- Skills: /review calls the repo's approver through the reviewer contract. /fix runs on changes_needed and repairs CI, conflicts, and review feedback, 3 passes max. /rank sorts findings by severity.
- Context: diff and approved plan, reviewer contract inputs, team standards and past review patterns.
- Exit gate: the verdict is approve, or a human approves per tier, and the PR has merged. An evidence link is attached to the item. Merging is the last step of Review: when every PR of an item is approved, the item takes a Release slot and its PRs merge. If no Release slot is free, the last approved PR keeps its Review slot until one frees, so a full Release stage backs up into Review and the wait is counted.

### 2.6 Release

- Purpose: UAT and go-to-market.
- Skills: /uat drafts scripts and collects sign-off. /gtm drafts notes, docs, and enablement. /rollout manages flags and stages. /cleanup removes flags and rollout scaffolding once the release is stable.
- Context: acceptance criteria, audience and launch calendar, flag configuration.
- Exit gate: UAT signed off at tiers 2 and 3, the rollout approved per tier (a human for tiers 2 and 3, notify and veto for tiers 0 and 1), and cleanup scheduled.

### 2.7 Monitor

- Purpose: watch, learn, and feed the backlog.
- Skills: /watch sets dashboards and alerts per release. /triage runs on a breach, ticket, or schedule. /learn turns findings into backlog items.
- Context: metrics and logs, support tickets, hypotheses written at plan time.
- Exit gate: findings logged to the backlog, and each hypothesis written at plan time marked proved or not.

## 3. Orchestrator and WIP limits

Pull, never push. A stage starts new work only when the next capped stage is under its limit.

- Defaults (Default): ready backlog 10, Plan 3, Execute 5 slices (local or cloud, never above the free Review slots), Review 8 (pipeline depth), 2 PRs in review per approver, Release 2. Test and Monitor have no cap because they are automated.
- A claim reserves a Review slot up front and takes an Execute slot. The Execute slot is released when the slice's tests are green and its PR moves into review. The Review slot is held until the PR is approved. So a slice never finishes with no reviewer capacity waiting, and Execute's effective limit is the smaller of its own limit and the free Review slots. Test has no cap of its own.
- Review is the throttle. A claim succeeds only if a Review slot is free. When Review is full, Execute stops pulling new slices. The Review limit measures pipeline depth, because a reservation is held through Execute and Test.
- Cloud dispatch means the orchestrator claims a free slot on a runner's behalf. It never pushes work onto a full stage.
- Backlog refills when below 6, from the ranked list. Plan pulls when an item leaves planning and Execute has room. Release pulls after review approval and a free slot. Monitor retires watches after the hypothesis window.
- Waiting on a human still counts as WIP. A flagged item keeps its slot, and a growing inbox is the signal to stop pulling.
- One expedite slot for incidents from Monitor. It may exceed a limit by one and must be logged.
- Limits are tuned in a monthly retro using review cycle time and blocked-item age. Raise a limit only after a stage stays idle.
- Scope: stage limits are set per workstream. A workstream is a board with a backlog: one team's board, or an initiative that spans teams. Two caps protect one person across every workstream they touch, and are ceilings, not capacity. The per-approver cap (Default 2) is the number of PRs ready for review that wait on one named approver. Because a Review slot is reserved at claim, it is what measures reviewer attention. A PR whose approvers are all at their cap stays a draft until one frees. The local runner cap is how many claims one person's runners may hold. Cloud Execute capacity is a workstream budget.

## 4. Work model and backward transitions

- Item: a unit of value with intent, an owner, and a predicted tier. It may span several repos. It counts against the Backlog and Plan limits.
- Slice: a one-session piece of an item with its own acceptance criteria. It counts against Execute from claim until its tests are green and its PR moves into review, and it reserves a Review slot at claim.
- PR: one per slice per repo touched, so slices do not stack and parallel slices never share a PR. Each PR gets its own effective tier from that repo's approver. Its slice reserved a Review slot at claim, held until the PR is approved. The item's effective tier is the highest among its PRs. No PR of an item with several PRs merges until all of them are approved, and they then merge together. An approved PR releases its Review slot while it waits for its siblings, so an item can have more PRs than Review slots. If an approved PR's branch changes, it needs a fresh Review slot and a new approval. The item releases when all its PRs have merged.
- Run: one claim attempt on a slice by a runner, with a lease and a bundle hash. The run holds the lease. The slice holds its Execute slot until its PR moves into review, and its Review reservation from claim.
- Waiting states (Decided), for items and slices alike:
  - Flagged: waiting on a human ask. Keeps every slot it holds.
  - Stalled: its run's lease expired. Keeps every slot it holds, and another runner can claim it. A stall that lasts past the stall timeout (Open) parks it.
  - Parked: an ask or a stall timed out. Frees every slot it holds and returns to the front of its stage's queue with handoff notes.

Backward transitions (Decided). Rework outranks new pulls for the next free Execute slot (Default, watch for starvation of new work).

- Plan rejected: returns to Plan and refine, or to the backlog if dropped. It keeps its Plan slot while reworked and frees it on drop. Decided by the plan approver.
- Test fails: returns to Execute on the same slice, which keeps its Execute slot and its Review reservation. Automatic.
- Review says changes_needed: goes to the fixer for up to 3 passes, then a human. It keeps its Review slot. Decided by the reviewer verdict.
- Human rejects the PR: returns to Execute with findings in the handoff notes. It keeps its Review reservation and reclaims an Execute slot ahead of new pulls. Decided by the approver.
- UAT fails: returns to Execute for a defect, or to Plan if the intent was wrong. Frees its Release slot. Decided by the UAT owner.
- Rollout or monitor breach: rolls back through the expedite lane, becomes a backlog item, and holds its Release slot until rolled back. Decided by on-call.
- Lease expires: the run is closed and the slice becomes stalled, keeping its Execute slot and Review reservation until another runner claims it or the stall timeout parks it. Automatic.

## 5. Humans in the loop

Humans are a flag and an inbox, not a stage.

- Every work item can carry a needs-human flag with an ask type, a one-line question, an owner, and time waiting.
- Ask types: Decide (priorities, rollback or fix forward), Answer (open questions, unblocking a runner), Approve (plans, PRs, rollouts), Verify (UAT, things automation cannot judge).
- The inbox is a query per person, oldest first, not a column.
- Every ask has a timeout: remind, then reassign, then park. Nothing waits silently.

Touchpoints by stage, with what the agent does while waiting and what happens with no answer:

- Backlog, Decide: the human sets the top of the ranked list and kills items. The agent keeps discovering and scoring. The queue stays as ranked.
- Plan, Answer: the human resolves open questions from /refine. The agent drafts slices for the confident parts. The item parks, freeing its Plan slot.
- Plan, Approve: the human signs off the plan and slices. The agent does nothing, since work is gated. Reminder, then escalate to the workstream owner.
- Execute, Answer: the human unblocks a runner that hit ambiguity. The run is released and the agent may claim another slice, while the flagged slice keeps its slots. The slice parks.
- Test, Verify: the human judges what automation cannot, such as UX feel. The agent runs remaining checks. The item stays red and does not advance.
- Review, Approve: tiers 2 and 3, the human approves the PR after reading ranked findings. Tier 1, the human skims ranked findings and merges, since there is no auto-merge at tier 1. Tier 0, the human can veto the automated approval before the auto-merge. The fixer repairs CI, conflicts, and feedback, 3 passes max. Reminder, then reassign the reviewer.
- Review, Answer: the human takes over after the fixer hits its cap, or settles a gap between predicted and effective tier. The agent holds the PR and logs the gap. Reminder, then reassign the reviewer.
- Release, Verify: tiers 2 and 3, the human runs UAT and signs off. The agent prepares GTM drafts. The rollout does not start.
- Release, Approve: tiers 2 and 3, the human approves the rollout through a hook. Tiers 0 and 1, the human is notified and can veto, otherwise the rollout proceeds once review is approved. The agent holds at the gate. For tiers 2 and 3 the rollout does not start without approval.
- Monitor, Decide: the human chooses to roll back, fix forward, or accept. The agent gathers evidence and proposes an option. On no answer, page on-call per policy.

## 6. Risk tiers and approvals

Scrutiny follows risk. Four tiers.

- Tier 0 Routine: docs, copy, dependency patches, flagged-off code. Evidence: green tests, no findings. Automated approval, then a short hold (length Open), then auto-merge where the policy allows, once every PR of the item is approved. The human is notified. A veto before merge revokes the approval. A veto after merge opens a revert item in the expedite lane, because an approval cannot be revoked once the change has merged.
- Tier 1 Standard: a contained feature or fix behind a flag. Evidence: green tests, confidence report, no blocking findings. Automated approval in active mode. Auto-merge is off at this tier. The human skims ranked findings and merges.
- Tier 2 Sensitive: shared services, schema changes, user-facing behavior. Evidence: full evidence plus UAT. The reviewer runs in shadow mode. One named approver reads the diff and approves.
- Tier 3 Critical: auth, payments, data migrations, public API. Evidence: full evidence, UAT, and a rollback rehearsal. Always-human guards. Two approvers, one a domain owner, with a staged rollout.

How a tier is set:

- Predicted at plan time by /refine from blast radius, reversibility, and data or security surface. The highest signal wins. A human confirms it at plan approval.
- The repo's approver computes the effective tier from the repo's rules during review and reports it with its verdict. It is computed again for every new head SHA and can only rise. The predicted tier is kept for comparison and logging only. A gap is logged and fed back to /refine.
- If the effective tier is higher than predicted, or a finding is blocking, a human is pulled in. Tiers never drop mid-flight.
- Earned trust (Default): a change type moves down a tier after a run of clean releases, for example 20 with no rollback and no escaped defects. A human grants the move and it is written down.
- Lost trust: an incident traced to an auto-advanced change moves that change type up a tier immediately and goes into the backlog as a learning item.

## 7. Review: one contract, swappable reviewers

Reviewer contract inputs: pr_ref (repo, PR number, head SHA), plan_ref (approved plan and acceptance criteria), and predicted_tier (logging only). An adapter may start a review or observe one the approver ran on its own GitHub events; either way the result is for that exact head SHA.

Outputs: verdict (approve, comment, changes_needed, or human_review), effective_tier (from the repo's rules, as the approver applied them), mode (shadow or active, as the repo's rules set it), findings (ranked by severity, each with file, line, and category), evidence_url (link to the audit record or dossier), and complete (false if any reviewer failed or the diff was truncated). If complete is false, the verdict becomes human_review. changes_needed triggers the fixer. comment means not approved: the PR goes to a human per its tier. An adapter reports the approver's tier and mode and never computes its own; an approver that cannot report a tier is advisory only.

Rules:

- The AI produces findings and deterministic rules owned by each repo decide. The AI cannot assert approval.
- Only one adapter per repo may approve. Others run in shadow and only comment.
- A crashed, partial, or truncated review is never treated as clean.
- A repo with no rules file gets the platform baseline guards and a default policy in shadow mode. It never auto-approves until the repo adds its own rules and switches to active.
- The fixer agent runs on changes_needed, makes up to 3 passes (Default), and the PR keeps its Review slot meanwhile. After the cap, a human is flagged.

Adapters (roles):

- Rules-gated approver: default approver. Rules decide, the AI only supplies findings, audit is replayable, and it is model-agnostic. Needs a per-repo rules file and a shadow soak.
- Hosted AI code review: advisory reviewer. Low setup, adjustable depth. Check how its findings map to your categories.
- Policy-file approver: candidate approver, shadow first. Approval policy kept in the repo. Check advisory-only mode, tier output, and data-retention fit (Unverified).
- Fixer agent: repairs, not a reviewer. Handles failing CI, conflicts, and review feedback. Check the iteration cap and slot accounting.
- Human only: fallback, used for tiers 2 and 3.
- Any other reviewer: advisory first. It can plug in once it passes a shadow bake-off against the current approver.

Tier to policy mapping (Default, a draft, written for a rules-gated approver with allow rules, guards and an AI review panel):

- Tier 0: docs-only and similar allow rules, AI review skipped, auto-merge allowed per policy, human notified and can veto.
- Tier 1: standard guards plus an allow rule on clean AI findings in active mode, no auto-merge, a human merges after skimming ranked findings.
- Tier 2: guards on paths such as schemas and shared services, or the AI-findings allow rule in shadow, no auto-merge, one named approver.
- Tier 3: always-human guards on payments, auth, migrations, and public API, AI review runs as advisory, no auto-merge, two approvers.

## 8. Runners: local and cloud

Both models run at the same time through one claim protocol.

- Cloud runners are dispatched work when the orchestrator claims a free slot on their behalf. Local runners pull work from the engineer's own agent tool.
- Claim protocol calls: claim (checks the stage WIP limit, grants a lease with a runner id, takes a slot), get_context (returns the context bundle), heartbeat (renews the lease and records progress), flag (sets needs-human with an ask type and question, the slot is kept), submit (links the branch or PR and evidence, moves the item toward Test), release (ends the run on abandon; the slice's slots are freed only when it parks).
- Keeping the tracker current: state comes from runner reports (proposals only), from git and GitHub events (a branch name or a Work-Item commit trailer ties a PR to an item, and PR opened, CI results, and review events move the item), and from leases and timers (a missed heartbeat expires the lease and the slice becomes stalled, as defined in section 4).
- Events are idempotent. A PR with no item is adopted by attaching or creating an item, flagged as untracked, and never rejected.
- Gates rely on verified evidence such as CI and the repo's reviewer, never on a runner saying tests passed.
- Local and cloud runs count against the same Execute limit. Each person's local runners are also held to the local runner cap.

Local vs cloud:

- Start: cloud is dispatched by the orchestrator. Local is pulled by the engineer's agent.
- Best for: cloud suits long, parallel, well-specified slices. Local suits private networks, local secrets, and exploratory work.
- Liveness: the platform tracks cloud jobs. Local relies on heartbeats and is lost when the laptop sleeps.
- Evidence trust: cloud runs in a controlled environment. Local evidence is a claim until CI confirms it.

## 9. Context bundle and resolution

Every runner receives the same bundle from one resolver, wherever work starts. The resolver is a context service, reached over MCP.

Bundle layers (kind, resolver rank in parentheses):

- Work item: id, title, intent, owner, predicted tier. Authored, context only. Source: tracker.
- Plan and slice: approved plan, this slice's scope, acceptance criteria, out of scope. Authored (rank 3). Owned by the plan approver.
- Repo guidance: AGENTS.md, repo skills, conventions files, code owners. Authored (rank 2). Read at the default branch tip. Owned by the repo team.
- Related work: dependency map, linked PRs, prior decisions, past review findings. Generated (rank 5).
- Constraints: allowed and protected paths, tool permissions, secrets policy, budget. Authored (rank 1). Set by the policy for the item's tier.
- Protocol: how to heartbeat, flag, and submit, plus branch name and commit trailer convention. Authored (rank 1). Owned by the platform.
- Handoff notes: progress, decisions, and open questions from earlier runs. Generated (rank 5). If a runner writes none, the tracker generates a baseline from git and CI events.
- Personal preferences: the engineer's own style and tool settings. Authored (rank 4).

Authored vs generated:

- Authored docs say what must be true, are written by people or approved agent output, and win conflicts. Examples: AGENTS.md, conventions files, the approved plan, acceptance criteria, rules and policy files.
- Generated docs say what appears to be true, are written by agents or tooling, always carry source commit and age, and lose conflicts. Examples: repo maps, dependency maps, architecture summaries, handoff notes, code summaries. Best used for orientation and filling gaps.

Precedence, highest first: tier constraints and policy, authored repo guidance, approved plan and slice, personal preferences, generated docs.

Conflict handling:

- Generated vs authored: the authored doc wins silently, a stale-doc finding is logged, and regenerating the doc becomes a backlog item.
- Authored vs authored: higher precedence wins and the runner is told which source it followed. A conflict between plan and repo guidance sets needs-human with an Answer ask.
- Local and cloud runners call the same resolver. Only layer 4, personal preferences, may differ between them, and the bundle hash records it.

Assembly rules:

- Small layers are inline. Large ones are references fetched on demand within a size budget (Open). Repo guidance is read at the default branch tip.
- Every reference is pinned by commit SHA or content hash at claim time. Each claim stores a bundle hash that includes the pins, so any result can be traced and replayed with the same bytes.
- Secrets never enter the bundle. Text from tickets, comments, and web pages is fenced as untrusted and cannot change constraints.

## 10. Local connector and tool adapters

Connector operations: next_work (maps to claim), get_bundle (get_context), report (heartbeat, sent automatically on an interval), ask_human (flag), submit_work (submit), drop_work (release), my_inbox (items flagged for this engineer).

Behavior:

- One protocol, two doors: an MCP server where a tool supports it, and a CLI everywhere else. Both call the same tracker API.
- Identity: the engineer signs in once and the connector holds a person token for next_work and my_inbox, which run before any claim. On claim it swaps to a token scoped to the claimed item only. Each session gets a runner id and a lease. Lease and heartbeat length are Open.
- Offline: events queue locally with ids and replay on reconnect. The tracker ignores duplicates and trusts git and CI events over late reports.
- Least privilege: the connector cannot approve, merge, or change limits. Those stay with the tracker and humans.

Adapter anatomy:

- Guidance file: tells the agent how to use the connector and where the bundle lives. AGENTS.md is the source, and tools that need their own file point to it.
- Connection: MCP config or CLI on the path, installed by one setup command per tool.
- Session hooks: start, stop, and commit events become report and submit calls. Optional, where the tool supports hooks.
- Git conventions: branch name and Work-Item trailer. Required for every tool. This is the fallback sync path.
- Conformance test: runs a fake item end to end through claim, report, flag, and submit. A tool is supported only when it passes.

Tool matrix (all Unverified, check before committing):

- Claude Code: CLAUDE.md that imports AGENTS.md. MCP, with hooks for session events. Check hook coverage for heartbeat and stop.
- Codex: AGENTS.md directly. MCP or CLI. Check background session lifecycle.
- Copilot agent: AGENTS.md or its own instructions file. MCP or CLI depending on the surface. Check which surfaces allow tool servers.
- Antigravity: AGENTS.md or its own rules file. MCP or CLI. Check rules file format and hook support.
- Any other tool: AGENTS.md, CLI plus git conventions only. Must pass the conformance test.

## 11. Routing between local and cloud

Signals, cloud vs local:

- Spec quality: a clear slice with testable acceptance criteria points to cloud. Ambiguous work needing back and forth points to local.
- Environment: reproducible in a clean container points to cloud. A private network, local services, or special hardware points to local.
- Secrets and data: none beyond what the sandbox may hold points to cloud. Credentials or data that cannot leave the machine points to local.
- Size and duration: long or highly parallel points to cloud. Short, exploratory, or interactive points to local.
- Tier: tiers 0 and 1 point to cloud. Tiers 2 and 3, where a person is close to the work, point to local.
- Engineer availability: away or at capacity points to cloud. Present and wanting to drive points to local.
- Cost: compute budget remaining points to cloud. A tight budget with a local seat already paid for points to local.

Rules:

- The orchestrator suggests a route at plan approval. The engineer can override it and the choice is logged.
- Hard limits beat preferences. Local-only data never goes to cloud. Tier policy can require a human nearby, which forces local.
- Work can move either way. A stalled cloud run can be taken over locally and a local run can be handed to cloud. Handoff notes and the bundle hash carry the state.
- Unclaimed eligible work: if nobody pulls it within a set time (one working day proposed, Default) and the policy allows, the orchestrator claims a free Execute slot for a cloud runner. It never exceeds the Execute limit.

## 12. Status of defaults and open items

Default (validate):

- WIP limits 10, 3, 5, 8, 2, per workstream, and 2 PRs in review per approver. Validate by watching idle time and queue age for a month.
- Fixer pass cap of 3. Track how often a third pass succeeds.
- Tier downgrade after 20 clean releases. Compare with rollback and escape rates.
- Unclaimed work timeout of one working day. Measure how long eligible items wait today.
- Rework outranks new pulls. Watch whether new work starves.
- A claim reserves a Review slot. Watch for Execute capacity idling while reviews are slow.
- Tier to policy mapping is a draft. Check against real rules files.

Open (decide):

- Tier 0 hold length before auto-merge.
- Heartbeat and lease length, and the stall timeout.
- Bundle size budget.
- Merge order across repos for items with several PRs.

Unverified (check):

- Per-tool support for MCP, hooks, and background sessions in each tool above.
- Whether any hosted policy-file approver can run advisory-only, report a tier, and fit data-retention constraints.

## 13. Build order and how to work

Build order:

- 1 Tracker and claim protocol: work items, leases, WIP slots, the needs-human flag, a CLI, and sync from git and CI events.
- 2 Review contract: wrap the existing rules-gated approver behind it in shadow mode so Review reports into the tracker.
- 3 Context resolver: serve the bundle and the authored-over-generated precedence for local and cloud.
- 4 Pilot with one team: two adapters that pass conformance, one cloud runner, one repo. Tune the defaults, then widen.

For the proof-of-concept, start with steps 1 and 2 only.

How to work:

- Vet first. Before building, list contradictions, missing pieces, and anything that cannot be built as described, and propose fixes.
- Verify the unverified. Where you can check a claim on this machine, such as a tool's MCP or hook support, check it and record the result.
- Keep the principles intact: agents propose and the tracker decides, rules decide approvals, authored beats generated, and incomplete is never clean.
- Record decisions and open questions in the repo as you go, and ask the human when this spec does not answer something.
