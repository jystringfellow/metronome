# Working on Metronome

Metronome is a control plane for AI-driven delivery work: claims, leases, WIP limits, human asks and gate
evidence. SPEC.md is the design and DECISIONS.md records what has been decided and what is still open.

## How to work

- Do not guess at anything SPEC.md marks Open or Unverified, or that DECISIONS.md lists as proposed. Ask the
  maintainer.
- Record each decision and each open question in DECISIONS.md, with its date, as you go.
- Keep the principles in SPEC.md section 1 intact: agents propose and the tracker decides, rules decide approvals,
  authored beats generated, and incomplete is never clean.

## Keep it generic

This repository is public, and anything in it, including its history, PR descriptions and review comments, stays
public. Write about the people and organisations who use Metronome only in general terms.

- Never name a user's or employer's company or team, or a private repository, internal host, internal service or
  internal tool, in code, tests, docs, examples, commit messages, PR descriptions, review comments or branch names.
  Public products and projects, such as GitHub, kapellmeister or scoring-stage, are fine to name.
- Describe an integration by its role, such as "a rules-gated approver" or "a context service", not by a private
  product's name. Organisation-specific adapters belong in a private repository.
- In tests and examples, use neutral names: `acme`, `example`, `team`, and hosts under `example.com`.
