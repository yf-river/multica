---
name: multica-working-on-issues
description: "Use when working on a Multica issue after the runtime has provided the trigger context — to apply the product contracts the runtime brief does not encode: how MR linking differs from close intent, how to read a linked MR's real state via the compatibility pull-requests CLI, which metadata keys are high-signal, what status changes trigger on the server, and how sub-issue create status (todo vs backlog) controls whether assigned agents start immediately."
user-invocable: false
allowed-tools: Bash(multica *), Bash(git *), Bash(gh *)
---

# Working on Multica issues

Product contracts the runtime brief does not fully encode: MR linking vs close
intent, reading linked MR state, metadata keys, status side effects, and
sub-issue enqueue behavior.

For building mention links, load `multica-mentioning` instead — not this skill.

Every contract below is traced to source in
`references/working-on-issues-source-map.md`.

## MR linking and close intent are two distinct contracts

The repository event sync runs two separate scans over an incoming MR. They are
not the same gate and they read different fields.

**Linking** scans the MR **title, body, OR branch** for a routable issue key
(`PREFIX-NUMBER`, e.g. `MUL-2759`). This scanning path is historical
compatibility only and must not be treated as the primary issue ↔ MR link
mechanism. Use `multica issue mr create` for new code-changing work, or
`multica issue mr link` when registering an MR that already exists outside the
platform. The link is what `multica issue mr list` reads back.

```text
MUL-2759: add built-in issue working skill        # title prefix → links
agent/matt/mul-2759-working-on-issues             # branch ref   → links
```

**Close intent** is explicit platform state, normally supplied by
`multica issue mr create --close-intent` or `multica issue mr link
--close-intent`. Historical provider webhooks may still mirror MR state, but
agents must not depend on title/body/branch text to create the issue link.

```text
Closes MUL-2759                                    # human-readable only; do not rely on it for platform linking
Fixes MUL-2759
Resolves MUL-2759
Fix login MUL-2759
```

Consequence: the reliable contract is the explicit platform MR record, not a
provider webhook guessing from text.

### Default for code-changing issue work

When an issue run changes code in a checked-out Gongfeng repo, the default handoff
is to create the MR through the Multica platform before posting the final issue
comment, unless the user explicitly asked for a local-only change or no MR. This
This is a default, not an unconditional command: if no code changed, say no MR is
needed; if MR creation is blocked by auth, failing tests, or missing remote
branch state, report that blocker instead of pretending the run is complete.

Push the source branch first, write the MR description to a UTF-8 file, then run:

```sh
multica issue mr create <issue-id> --provider gongfeng --project-path <project-path> --source-branch <branch> --target-branch <target-branch> --title "<title>" --description-file <path> --output json
multica issue mr list <issue-id> --output json
```

The `mr create` command creates the provider MR and synchronously records the MR
on the issue. Do not rely on MR title, body, or branch identifiers as the primary
linking mechanism. If an MR already exists because it was created outside the
platform, register it explicitly with `multica issue mr link`.

In the final issue comment, include the MR URL when an MR exists. If the task did
not produce an MR because no code changed or the user asked not to create one, say
that explicitly.

## Reading a linked MR's real state

When a step depends on MR state, query Multica's link table — do not infer it
from branch names, memory, or `mr_url` metadata (which can be
stale).

```bash
multica issue mr list <issue-id> --output json
```

Returns `{"pull_requests": [...]}` for compatibility. Each element exposes:

- `number`, `html_url`, `title`
- `state` — the MR lifecycle as a **single enum**, one of `merged`, `closed`,
  `draft`, `open`. There is no separate `draft` or `merged` boolean in the
  response; the server folds them into `state` (merged wins, then closed, then
  draft, else open).
- `merged_at` — non-null once merged; a second confirmation of `state: merged`.
- `mergeable_state` — mirrors the repository provider (`clean` / `dirty` surfaced; other values
  round-trip as unknown).
- `checks_conclusion` — aggregated CI: `passed`, `failed`, `pending`, or `null`
  when no check suite has been observed. Backed by `checks_passed`,
  `checks_failed`, `checks_pending` counts.

So "is it merged?" is `state == "merged"` (or `merged_at != null`); "is it still
a draft?" is `state == "draft"`; CI status is `checks_conclusion`.

If the command returns no linked MRs after an MR was opened, the link scanner did
not observe a routable issue key in the MR title/body/branch.

## Metadata: high-signal keys only

Metadata is durable issue state. Reading metadata is safe. Writing a metadata key
is a state mutation and should be tied to an explicit task requirement to record
that state for later readers or runs.

High-signal keys (reuse these names so queries stay consistent):

- `mr_url`
- `mr_number`
- `pipeline_status`
- `deploy_url`
- `external_issue_url`
- `waiting_on`
- `blocked_reason`
- `decision`

Not metadata: logs, summaries, files touched, timestamps, attempt counts,
investigation notes. Those belong in the result comment.

```bash
multica issue metadata set <issue-id> --key mr_url --value <url>
multica issue metadata delete <issue-id> --key <stale-key>
```

`--value` is JSON-parsed by default (bool/number are sniffed); pass `--type
string|number|bool` to force a type.

## Status changes have server side effects

A status change is not cosmetic — the server enqueues or skips agent work based
on it. These are the contracts, not advice:

- **`backlog`** parks an agent-assigned issue: the assignee is set but no task
  fires. Moving `backlog → todo` (or any non-done/non-cancelled status) enqueues
  the assigned agent then.
- **`in_review`** is an accepted issue status. Some workflows use it while an MR
  is open and awaiting review; moving to it is an explicit mutation.
- **`done`** on a child issue posts a system comment on its parent. If an MR
  carries close intent (`Closes MUL-XXXX`), it advances the issue to `done`
  itself on merge — you do not also need to flip it manually.
- **`done`** on a parent issue is blocked while any child issue is not `done`.
  Finish, cancel, or explicitly resolve the child work first; if the platform
  rejects the status change, report the unfinished child list instead of
  forcing completion.
- **`cancelled`** stops outstanding work; treat it as a user-driven decision.

## Sub-issues: `todo` starts work now, `backlog` parks it

On an agent-assigned issue, create status decides whether the assignee fires
immediately. A non-backlog status (e.g. `todo`) enqueues the agent at create
time; `backlog` sets the assignee without triggering.

Parallel children — all start now:

```bash
multica issue create --title "..." --parent <issue-id> --assignee <agent> --status todo
```

Strictly serial children — park later steps, promote one at a time:

```bash
multica issue create --title "Step 2: ..." --parent <issue-id> --assignee <agent> --status backlog
multica issue status <child-id> todo   # promote when the previous step is truly done
```

Creating every serial step as `todo` enqueues the whole chain at once.

## Incorrect → correct

MR title (link the issue):

```text
Fix login redirect                  # incorrect — no issue key, won't link
MUL-2759: fix login redirect        # correct — links the MR
```

Serial sub-issues (don't start the whole chain):

```bash
# incorrect — both fire immediately
multica issue create --title "Step 2" --parent <issue-id> --assignee <agent> --status todo
multica issue create --title "Step 3" --parent <issue-id> --assignee <agent> --status todo

# correct — parked, promote in turn
multica issue create --title "Step 2" --parent <issue-id> --assignee <agent> --status backlog
multica issue create --title "Step 3" --parent <issue-id> --assignee <agent> --status backlog
```

## References

`references/working-on-issues-source-map.md` — accurate `file:line` for every
contract above: the `pull-requests` CLI and route, the MR response field list,
`derivePRState`, the two-path link (`extractIdentifiers`) vs close-intent
(`extractClosingIdentifiers`) proof, the backlog enqueue lines, child-done
notify, and the metadata CLI. Re-derive before depending on an exact line.
