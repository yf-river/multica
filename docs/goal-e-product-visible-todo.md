# Goal E Product-Visible TODO

Generated: 2026-06-26T13:49:00+08:00

## Closed In This Slice

- Project resource rows now expose Gongfeng actions in the project page: test connection, sync, disable/enable, and delete.
- Remediation seed status no longer writes `seeded_for_remediation`, `pending_verification`, or `requires_real_click_acceptance` as fake completion states.
- `goal-test-daemon` now has a retained canonical demo scenario:
  - issue: `84fcd1c0-2dd6-4f19-aa52-ecbfa761bf1b`
  - six role task nodes are present for PM, 01-clarify, 02-design, 03-task-split, 04-implement, 05-verify
  - eval run, optimizer candidate, skill apply + CHANGELOG, and re-eval evidence exist
  - latest runtime artifact: `artifacts/acceptance/goal-e-canonical-demo-seed-latest.json`
- Final acceptance no longer falls back to `archived_final_acceptance_artifact` for PM+01-05 stage evidence.
- Final evidence package now requires both current canonical demo evidence and a separate real PM+01-05 model execution artifact.
- Real PM+01-05 model execution has completed on `goal-test-daemon`:
  - issue: `a68cb7e2-270a-4652-a7f8-98a3de6e8aff` / `GOA-450`
  - model: `gpt-5.3-codex-spark`
  - fallback used: `false`
  - stages: PM, 01-clarify, 02-design, 03-task-split, 04-implement, 05-verify all completed with nonzero duration, messages, trace events, and token usage
  - latest runtime artifact: `artifacts/acceptance/goal-e-real-pm-0105-run-latest.json`
  - final acceptance now consumes this artifact for `P0-05` and `P0-12`.

## Still Open P0

- Complete the cross-project SOP proof: usercenter parent creates gateway and ida-deployment children, owner approval moves backlog to todo, siblings can run in parallel, and parent wakes only after all children finish.
- Run a deployed Playwright product audit after clean deploy against the current web pages, not only historical E2E workspaces.
- Run `verify-logs int` after the final browser audit.

## Current Blocking Evidence

- `node scripts/generate-tapd-gongfeng-sop-final-acceptance.mjs` currently fails on:
  - `E-10`: deployed server/web/daemon/runtime logs and key page performance are not yet clean for the unified acceptance window
  - `E-11`: final evidence package still lacks the post-deploy browser/log/performance bundle
- `node scripts/generate-goal-e-final-evidence-package.mjs` now fails only on `E-10` and `E-11`.

## Next Command

Run the real PM+01-05 model path after the current code is committed and deployed. The command now triggers the real user-center squad issue, PM, and 01-05 agent tasks, then hard-verifies task messages, usage, trace, and SOP stage metrics:

```bash
node scripts/run-goal-e-real-pm-0105.mjs
```

Do not mark `demo-ready` until final acceptance, gap audit, final package, deployed browser audit, and post-browser log scan all pass.
