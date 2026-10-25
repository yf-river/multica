---
name: multica-working-on-issues
description: "Use when acting on a Multica issue beyond what the brief covers: PR linking vs close intent, reading a linked PR's real state, metadata keys, status-change side effects, sub-issue todo vs backlog."
user-invocable: false
allowed-tools: Bash(multica *), Bash(git *), Bash(gh *)
---

# Working on Multica issues

Product contracts the runtime brief does not fully encode: PR linking vs close
intent, reading linked-PR state, metadata keys, status side effects, and
sub-issue enqueue behavior.

For building mention links, load `multica-mentioning` instead — not this skill.

Every contract below is traced to source in
`references/working-on-issues-source-map.md`.

## PR linking and close intent are two distinct contracts

The GitHub webhook runs two separate scans over an incoming PR. They are not the
same gate and they read different fields.

**Linking** scans the PR **title, body, OR branch** for a routable issue key
(`PREFIX-NUMBER`, e.g. `MUL-2759`). Each match writes an issue ↔ PR link row.
This is the link that `multica issue pull-requests` reads back — but see the
reference-only rule below: a key that appears **only** as a bare mention in the
body is linked yet hidden from that list.

```text
MUL-2759: add built-in issue working skill        # title prefix → links, shown
agent/matt/mul-2759-working-on-issues             # branch ref   → links, shown
```

**Close intent** is stricter and is a separate scan over **title or body only —
never the branch**. It fires only for a key placed immediately after a closing
keyword (`Closes` / `Fixes` / `Resolves`, optional `:` then whitespace). That
adjacency is what sets the link row's close-intent flag, the gate that
auto-advances the issue to `done` when the PR merges.

```text
Closes MUL-2759                                    # links AND records close intent
Fixes MUL-2759
Resolves MUL-2759
Fix login MUL-2759                                 # links only — keyword not adjacent
```

Consequence: a bare title prefix or a branch reference links the PR but does not
close the issue on merge. A closing keyword immediately adjacent to the issue key
records close intent; on merge, that close intent can move the linked issue to
`done`.

**Reference-only links (hidden from the PR list).** A key that appears **only**
as a bare mention in the body — no closing keyword, and not in the title or
branch — still writes a link row, but the row is flagged `reference_only` and
**excluded from `multica issue pull-requests`** (and the issue's right-side PR
list in the UI). This keeps passing mentions like `Related MUL-2759` or
`Follow up in MUL-2759` from surfacing an unrelated PR as if it were working on
that issue. To make a PR show up for an issue, put the key in the title, the
branch, or after a closing keyword in the body — not as a loose body reference.

```text
Closes MUL-2759 in the body                        # links and shown
Related to MUL-2759 in the body (no title/branch)  # links but reference_only → hidden
```

### Default for code-changing issue work

Work in the target checkout and report the changes and verification. Commit,
push, PR creation, and deployment follow the user's authorization and applicable
project rules. Create downloadable files or archives only when needed by the
requested delivery. The platform saves the final answer as an issue comment;
explicit CLI replies are for attachments or replies to multiple threads.

Use a routable issue key in the PR title, body, or branch so the webhook can link
the PR back to the issue. If the PR should close the issue on merge, put the key
immediately after a closing keyword in the title or body, for example:

```text
MUL-2759: fix login redirect        # links only
Closes MUL-2759                     # links and records close intent
```

In the final issue comment, include the PR URL when a PR exists. If the task did
not produce a PR because no code changed or the user asked not to create one, say
that explicitly.

## Reading a linked PR's real state

When a step depends on PR state, query Multica's link table — do not infer it
from branch names, GitHub search, memory, or `pr_url` metadata (which can be
stale).

```bash
multica issue pull-requests <issue-id> --output json
```

Returns `{"pull_requests": [...]}`. Each element exposes:

- `number`, `html_url`, `title`
- `state` — the PR lifecycle as a **single enum**, one of `merged`, `closed`,
  `draft`, `open`. There is no separate `draft` or `merged` boolean in the
  response; the server folds them into `state` (merged wins, then closed, then
  draft, else open).
- `merged_at` — non-null once merged; a second confirmation of `state: merged`.
- `provider` — `github`, `forgejo`, `gitea`, or `gitlab`.
- `mergeable_state` — mirrors GitHub (`clean` / `dirty` surfaced; other values
  round-trip as unknown; retained for compatibility).
- GitHub API snapshot fields: `snapshot_available`, `mergeable`,
  `merge_state_status`, `checks_rollup`, `checks_total`, `checks_passed`,
  `checks_failed`, `checks_running`, `failed_check_names`,
  `snapshot_fetched_at`, and `snapshot_stale`. `snapshot_available == true`
  means the feature is enabled and the snapshot matches the PR's current head.
  Only then does `checks_rollup == null` mean "no checks"; false means the
  snapshot feature is disabled, has not fetched yet, or only has an old head.
- `checks_conclusion` — coarse CI compatibility status: `passed`, `failed`,
  `pending`, or `null`. GitHub derives it from the current API snapshot;
  Forgejo/Gitea/GitLab derive it from webhook commit statuses. Backed by the
  provider-appropriate check counts.

So "is it merged?" is `state == "merged"` (or `merged_at != null`); "is it still
a draft?" is `state == "draft"`; coarse CI status is `checks_conclusion`.

If the command returns no linked PRs after a PR was opened, the link scanner did
not observe a routable issue key in the PR title/body/branch — or the only match
was a bare body mention, which links as `reference_only` and is hidden from this
list (see the reference-only rule above).

## Metadata: durable custom state

Metadata is a free-form KV bag of durable issue state. Reading metadata is safe.
Writing a metadata key is a state mutation and should be tied to an explicit
task requirement to record that state for later readers or runs. Keys are
whatever your workflow needs — the platform curates no vocabulary; pick short
snake_case names and reuse them consistently within your workspace.

Never store secrets, tokens, or API keys in metadata.
Not metadata: logs or summaries; runtime bookkeeping such as timestamps,
attempt counts, or agent IDs; or other single-run details such as
files touched and investigation notes — those belong in the result comment.

```bash
multica issue metadata set <issue-id> --key <key> --value <value>
multica issue metadata delete <issue-id> --key <stale-key>
```

`--value` is JSON-parsed by default (bool/number are sniffed); pass `--type
string|number|bool` to force a type.

## Custom properties: typed workflow state

Workspaces may define custom issue properties (Severity, Environment, QA
Status, Reviewer, ...). Properties are the typed, user-visible sibling of
metadata: values are validated against the definition (select options, date
format, http(s) URL, member reference), visible in the issue sidebar, and
addressed by name.

- Read what exists before writing: `multica property list` shows the catalog;
  `multica issue property list <issue-id>` shows values set on the issue.
- Set values by property name and option name — the CLI translates to ids:

```bash
multica issue property set <issue-id> --name Environment --value staging
multica issue property set <issue-id> --name Platforms --value "iOS,Android"
multica issue property set <issue-id> --name Reviewer --value Bohan
multica issue property unset <issue-id> --name Environment
```

- A validation error lists the legal options — fix the value and retry.
- `actor` / `multi_actor` properties (Reviewer, Escalation contact, ...) hold
  workspace members only. `--value` takes a member name, email, UUID, short id,
  or an explicit `member:<uuid>`; `multi_actor` takes a comma-separated list
  (duplicates dropped, order kept, max 20).
- Definitions may include an optional catalog icon for visual identification;
  it does not change the property's type or value validation.
- Agents cannot create or edit property definitions (owner/admin humans only).
  If a needed property does not exist, propose it in a comment instead.
- Property vs metadata: if the value is workflow state a human should see and
  filter by, and a definition exists, prefer the property. Metadata stays the
  free-form bag for durable custom issue state.

## Status changes have server side effects

A status change is not cosmetic — the server enqueues or skips agent work based
on it. These are the contracts, not advice:

A workspace may define custom statuses beyond the seven built-ins; when any
exist, the runtime brief's Available Commands section lists this workspace's
catalog. A custom status inherits its category's behavior in full, and each
built-in key below is also the name of its category — so read these bullets as
category rules. Two writes are literal-key exceptions, not category rules: the
failed-task rollback below writes the literal `todo` key, and a merged PR with
close intent writes the literal `done` key.

- **`backlog`** parks an agent-assigned issue: the assignee is set but no task
  fires. Moving `backlog → todo` (or any non-done/non-cancelled status) enqueues
  the assigned agent then.
- **`in_progress` / `in_review`** are agent-managed CLI mutations, not
  `StartTask` / `CompleteTask` side effects. The runtime brief asks agents to
  write the state the issue is in whenever their work changes it — not from
  the trigger type or the run's lifecycle, and not gated on being the
  assignee. Writes happen whenever the state changes, mid-turn included: a
  turn that advances the issue's own ask sets `in_progress` as soon as that
  is known, so the board shows the work while it runs; a blocker is recorded
  when it is hit; and the turn must not exit with a stale value — delivered
  the issue's own ask with necessary checks complete → `in_review` for human
  review. Set `done` directly, with no further user confirmation, only when
  all three hold: the user authorized production release for this issue, the
  accepted candidate was actually released to production, and real business
  acceptance then passed on that released version. Test-environment
  acceptance, a successful build, or a deployment that merely starts never
  suffices, and a failed release or a failed acceptance leaves the issue out
  of `done`. Issues without release authorization, and analysis, configuration,
  or other non-release work, keep their own completion conditions. Work continues beyond the turn
  (dispatched sub-issues, partial delivery) → `in_progress`; stuck →
  `blocked`. A turn that produces none of the issue's own deliverable —
  answering a question, consulting on work owned elsewhere — writes nothing
  at any point. The kind of activity never decides this: research, design,
  planning, and review all count as the work exactly when they are what the
  issue asks for (a review-the-PR issue is being worked the moment reviewing
  starts). Questions, discussion, or acknowledgements never move the status.
  Squad leaders: dispatching members is not delivery — a dispatch turn
  leaves the parent `in_progress`, and it moves to `in_review` only when a
  later re-trigger confirms the overall goal is met.
- **`in_review`** is an accepted issue status. Some workflows use it while a PR
  is open and awaiting review; moving to it is an explicit mutation.
- **`done`** on a child issue posts a system comment on its parent. If a PR
  carries close intent (`Closes MUL-XXXX`), it advances the issue to `done`
  itself on merge — you do not also need to flip it manually. In workspaces with
  quality supervision enabled, `done` also opens one review round over the
  completion snapshot (materials registered before that moment); it does not
  start new work on the issue, and reopening stops the round.
- **`cancelled`** is a terminal, user-driven decision to close the issue. Like
  `done` it enqueues no new agent work, but it does **not** stop tasks already in
  flight — a run in progress keeps going (MUL-4465). To stop a running task,
  cancel the task itself.
- **Failed issue-triggered tasks** may roll an issue from `in_progress` back to
  `todo` when no active task / retry remains — that is the main server-owned
  status write on the agent-run path.

## Claim ownership without duplicating a run

Assigning an active issue to an agent normally starts a run. When the work is
already underway and the write only records ownership or progress, pass
`--no-start` on every command in that flow — suppressing the assignment alone
does not suppress a later status update:

```bash
multica issue assign <issue-id> --to-id <agent-id> --no-start
multica issue update <issue-id> --assignee-id <agent-id> --no-start
multica issue status <issue-id> in_progress --no-start
```

Before self-assigning, check the target issue's comment history for an existing
claim and any `## Active sibling runs` block (its `run-messages` commands show
work in flight). The server also suppresses a trusted self-assignment when the
exact target `(issue, agent)` pair already has a non-terminal task, but it
deliberately keeps same-agent handoffs to a fresh issue starting runs: cross-issue
serial chains and triage batches rely on that.

## Who else is running right now

The `## Active sibling runs` block covers only YOUR OWN other in-flight tasks.
It says nothing about another agent working next to you, and it is a claim-time
snapshot that does not refresh mid-turn. Ask the server when you need the
current answer:

```bash
multica issue runs <issue-id> --active --output json     # in-flight runs on this issue
multica issue runs <issue-id> --siblings --output json   # ...and across the sub-issue family
```

`--active` drops the execution history and returns only `queued` / `dispatched`
/ `running` / `waiting_local_directory` runs. `--siblings` widens the same read
to the issue's family — its parent (or itself, when it has no parent) plus every
child of that parent — and labels each row with the issue it belongs to, which
is how you find another agent already working on a sibling sub-issue before you
open a second PR against the same code.

The family read returns a compact row — task, issue, agent, status, started —
not the full execution-log record. If you need a run's detail, follow the task
id with `multica issue run-messages`.

Rows come back running-first, newest-first within a status, and the family read
is capped at 20. When the cap truncates the answer the CLI prints a warning on
stderr — read it. Without that warning a short list means "nobody else is
there"; with it, the list proves nothing about the runs it did not return.

Both are advisory reads. Nothing here reserves an issue or serialises anything:
a run you see may finish a second later, and one you don't see may start a
second later. Coordinate through the issue's comments — the reads tell you whom
to coordinate with.

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

### Stages: order sub-issues into barrier groups

`--stage <N>` (N ≥ 1) groups sub-issues under the same parent into ordered
stages. The parent assignee is woken **once, when a whole stage finishes** —
i.e. every sub-issue in the lowest unfinished stage has reached a terminal
status (`done`/`cancelled`). A completion that does not close a stage is silent
(no comment, no wake). A sibling set with **no** stages is one implicit stage,
so the parent is woken once when the *last* sub-issue finishes — not on every
child.

Advancement is agent-driven: the server only detects the closed barrier and
wakes the parent assignee, who then decides whether to promote the next stage's
`backlog` sub-issues to `todo`.

```bash
# Stage 1 runs now; later stages parked until promoted
multica issue create --title "Research A" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Research B" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Build"      --parent <id> --assignee <agent> --stage 2 --status backlog
multica issue create --title "Ship"       --parent <id> --assignee <agent> --stage 3 --status backlog
```

When both Stage 1 sub-issues finish you (the parent assignee) are woken with a
"Stage 1 complete" comment. Inspect the layout, then promote the next stage:

```bash
multica issue children <parent-id>             # sub-issues grouped by stage
multica issue status <stage-2-child-id> todo   # promote when its deps are met
```

`issue children --output json` reports per-stage `done` counts. A workspace may
define custom statuses beyond the 7 built-ins; a custom status counts as done
here when its category is `done` or `cancelled`, which is what `status_category`
on each child carries. Read `status_category` rather than matching `status`
against the built-in names.

Read each sub-issue's description before promoting and only promote items whose
stated dependencies are met; if a description conflicts with the parent's
breakdown, leave it `backlog` and comment to confirm first.

## Incorrect → correct

PR title (link the issue):

```text
Fix login redirect                  # incorrect — no issue key, won't link
MUL-2759: fix login redirect        # correct — links the PR
```

Serial / phased sub-issues (don't start the whole chain at once):

```bash
# incorrect — all fire immediately, no ordering
multica issue create --title "Step 2" --parent <issue-id> --assignee <agent> --status todo
multica issue create --title "Step 3" --parent <issue-id> --assignee <agent> --status todo

# correct — stage them; Stage 1 runs, later stages park and are promoted as
# each stage's barrier closes
multica issue create --title "Step 1" --parent <issue-id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Step 2" --parent <issue-id> --assignee <agent> --stage 2 --status backlog
multica issue create --title "Step 3" --parent <issue-id> --assignee <agent> --stage 3 --status backlog
```

## References

`references/working-on-issues-source-map.md` — accurate `file:line` for every
contract above: the `pull-requests` CLI and route, the PR response field list,
`derivePRState`, the two-path link (`extractIdentifiers`) vs close-intent
(`extractClosingIdentifiers`) proof, the backlog enqueue lines, child-done
notify, the stage column / `stageBarrierClosed` barrier and the `--stage` /
`issue children` CLI, and the metadata CLI. Re-derive before depending on an
exact line.

### Inspect complete tool output

`multica issue run-messages <task-id>` returns output previews. When
`output_truncated` is true, use the message `id` to retrieve the stored output:

```bash
multica issue run-messages <task-id> --message-output <message-id> --offset 0
multica issue run-messages <task-id> --message-output <message-id> --search 'error'
```

Offsets count Unicode characters. Continue with `next_offset` while `has_more`
is true. Search starts at `offset` and checks the full stored output. These
records contain the provider-delivered output after secret redaction; historical
outputs truncated before storage cannot be recovered through this command.

## Multica delivery ownership and release pauses

For Multica code delivery, use `multica issue delivery` with the current verified `MULTICA_TASK_ID` when running as an Agent. An explicit `--execution` must equal that task ID; a missing or mismatched trusted identity is rejected. External member operators use a stable `CODEX_THREAD_ID` or `--execution` and renew their own owner lease. Reuse the existing delivery for the same plan and source commit. Inspect `owner` and delivery history before any recovery; another execution requires `recover-owner --recovery-evidence` and must not overwrite active work. Keep the existing source branch and acceptance evidence.

While a legitimate external member owner lease is live, prepared and batch-ready notifications stay pending and a side native run ending cannot abort its candidate. Releasing or expiring that lease restores native notification and failure handling. Native stage and release-resume runs remain native; do not impersonate a member or change the issue assignee to bypass this protocol. Technical failures and the complete business checklist still apply.

Bind the confirmed plan’s business checklist as `required_checks` on delivery submission: each item has `name` and `stage` (`verification` or `integration`), with at least one item per stage. For an existing unpublished delivery missing this list, use `delivery bind-checks` with its `plan_id`, `revision` and checklist; do not request the same user authorization again or weaken already bound checks. Platform coordination checks are not business acceptance. Submit evidence for the exact candidate covering every required item at that stage; a batch representative covers every member’s integration items. Missing checks require real testing, not renaming HTTP or settings checks as completed business flows. See `docs/operations/environment-releases.md` for JSON examples.

Submit actual acceptance evidence, then finish this execution. The external publisher advances the release and posts the next-stage result; do not poll synchronously for the restart of your own daemon. A publication pause is not user cancellation: preserve the original task and work directory. On automatic continuation, inspect Git refs, delivery state, deployment identity, and the last external operation before resuming; never repeat an operation with an unknown result blindly. Report a precise task-level blocker if its outcome cannot be established.

When the review notification says an existing release authorization has not been reviewed for the current candidate, read the latest user comments and the delivery's `authorizations`, `user_comments_version` and `authorization_state`, then bind that authorization to the candidate with `approve` (`authorization_id`, `scope_review`, `user_comments_version`). Do not ask the user to repeat an authorization they already gave, and do not approve on their behalf: stop and explain the reason when the scope changed, the candidate changed materially, or the authorization was revoked. A missing review, a queue notification that has not been delivered, and a review run that failed are different states — report the one you actually observe instead of calling the delivery automatically continuing.

## 托管代码审查

工作区管理员在审核中列的“审查设置”选择运行时、模型与提示词。启用后，任何进入 `in_review` 的路径（手动或批量改状态、交付验收通过）都会派发托管审查员；开发者完成修改后提交审核，不手动调用或指派该隐藏 Agent。派发失败或进程重启丢掉的那一轮由服务端补扫补齐，不需要也不应该手工补派。

补扫不会重复审查的情形：已有活动的审查执行、审查已通过且被审提交没有变化、上一轮打回后没有新提交、审查已因停滞或轮次上限停止等待人工处理、本轮审查已经执行但没有提交结论（没有关联 PR 时无法证明有改动，不重跑同一轮）。这几种情况下任务停在审核中不是漏派，改状态或重复触发只会打断已有结论。

审查员只接受系统派发的审查轮次，普通评论和 @ 都不会启动它。工作区成员在审查结论线程里回复时，服务端把这条回复交给任务的**当前负责人**（负责人已更换则给新负责人），用户不需要再 @ 一次；回复仍留在原线程，讨论关系不变。Agent 自己 @ 审查员不会转给负责人，会被按不可调用拦下；要负责人接手时直接 @ 负责人，或请用户在该线程回复。

审查员通过当前任务令牌提交结论：

```bash
multica issue review submit <issue-id> --verdict pass --findings-file /absolute/findings.json --output json
multica issue review submit <issue-id> --verdict reject --findings-file /absolute/findings.json --comment-id <review-comment-id>
multica issue review submit <issue-id> --verdict stalled --findings-file /absolute/findings.json
```

文件格式为 `{ "summary": "结论", "items": [{ "level": "blocking", "file": "path", "detail": "实际问题" }], "unchanged_previous": [] }`。`pass` 写通过标记，任务留在审核中；`reject` 回到进行中并唤醒原开发者；`stalled` 或达到累计轮次上限会停止自动返工并请负责人处理。只有配置的审查 Agent 和有效的当前执行可提交，旧轮次不能覆盖新轮次。不要用直接修改状态代替提交审查结果，也不要将审查通过等同于生产发布完成。

When a `batch_ready` notice names a combination candidate and representative delivery, validate that fixed candidate across all member impacts and integration smoke flows. The representative submits actual combined evidence with `multica issue delivery accept`, including the current `revision`, exact `candidate_id`, and `evidence` in its JSON request. Other members assist validation without creating duplicate deliveries or independent releases. Single-task history and platform coordination checks alone are not combined business acceptance.

### Creating acceptance-test Agents

Production Agent settings belong to the user. Test Agents follow the production Settings → Test environment agents policy, including environment defaults and explicit Agent overrides. New Agents and seed restore use the current test default through `scripts/test-agent-policy.py`; creator model identity does not override it. Read `docs/operations/environment-releases.md` → 测试 Agent 模型维护. Missing policy, unsupported runtime/model or persistent authentication failure requires human assistance; preserve active work and never switch provider automatically.
