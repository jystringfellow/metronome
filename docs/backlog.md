# Backlog JSON contract

Metronome reads ranked work from an external backlog; it does not own that backlog. Supply this JSON
to `metronome backlog sync [--workstream NAME] FILE` or `POST /v1/backlog/sync`.

## Request fields

| Field | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `workstream` | string | No | `default` | Target workstream. Must agree with `--workstream` when both are supplied. |
| `items` | array of items | No | empty | Ranked by position; the first item has rank 1. |

## Item fields

| Field | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `ref` | string | Yes | — | Backlog reference, unique across workstreams. Known refs are skipped. |
| `title` | string | Yes | — | Item title. |
| `intent` | string | No | `""` | Why the work is needed. |
| `owner` | string | Yes | — | Person responsible for the item and its plan approval. |
| `value` | number | No | `0` | Item value. |
| `predicted_tier` | integer, 0 to 3 | No | `1` | Tier proposed for confirmation. If absent or `null`, the approval ask explains the default; explicit `0` stays 0. |
| `slices` | array of slices | No | empty | Pre-planned slices. Absent, `null` or `[]` means the plan must be proposed in Plan. |

## Slice fields

These are the same fields as each slice in the array accepted by `plan propose ITEM --slices FILE`.

| Field | Type | Required | Default | Meaning |
| --- | --- | --- | --- | --- |
| `title` | string | Yes | — | Slice title. |
| `repo` | string | Yes | — | Repository, in `OWNER/NAME` form. |
| `criteria` | array of strings | No | empty | Slice acceptance criteria. |
| `approvers` | array of strings | No | empty | GitHub logins whose reviews count; when empty, anyone's review counts. |
| `after` | array of integers | No | empty | 1-based positions of other slices in this item's array that must merge first. |

Required strings must be nonempty. Dependency positions must be in range, distinct, not refer to the
slice itself and not form a cycle. Unknown JSON fields are refused. All items are validated before refill,
even known refs; an invalid slice refuses the entire sync and names its item by ref.

Refill adds items only when Ready is below the refill threshold, up to the workstream's Ready limit.
Known references are skipped in their entirety, including any new slices or tier in the request.
`plan start` proposes pre-planned slices and raises an approval ask for the owner. A person can replace
the proposal with `plan propose`. `plan approve ITEM [--tier N]` approves the plan and confirms the tier;
without `--tier`, the predicted tier is confirmed.

## Example

```json
{
  "workstream": "default",
  "items": [
    {
      "ref": "ACME-1",
      "title": "Add export",
      "intent": "Let people save their records",
      "owner": "pat",
      "value": 5,
      "slices": [
        {"title": "Export API", "repo": "acme/app"},
        {
          "title": "Export page",
          "repo": "acme/web",
          "criteria": ["Records can be saved as a file"],
          "approvers": ["rev"],
          "after": [1]
        }
      ]
    },
    {"ref": "ACME-2", "title": "Update docs", "owner": "pat"}
  ]
}
```

Changes to this contract are additive only: fields may be added, and existing fields keep their meaning
and defaults.
