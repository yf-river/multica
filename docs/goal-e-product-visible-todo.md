# Goal E Product-Visible TODO

Generated: 2026-06-26T15:24:30+08:00

## Current State

- Status: `demo-ready`
- Deployed environment: int
- Deployed URL: `http://9.134.129.162:13682`
- Deployed commit: recorded by `.run/deployment-int.json` and the final evidence package for the current deployment
- Final evidence package: `artifacts/acceptance/goal-e-final-evidence-package-latest.json`
- Final acceptance: `artifacts/acceptance/tapd-gongfeng-sop-final-acceptance-latest.json`
- Gap audit: `artifacts/acceptance/tapd-gongfeng-sop-gap-audit-2026-06-26T07-24-06-401Z.json`

## Closed

- Active projects are limited to `usercenter`, `gateway`, and `ida-deployment`.
- Active agents are limited to `PM`, `01-clarify`, `02-design`, `03-task-split`, `04-implement`, and `05-verify`.
- The default training/evaluation agent path now reuses `05-verify` when the PM+01-05 squad exists, so the training loop stays closed without creating a seventh active agent.
- The Gongfeng resource list/actions are visible from project resources, and the Gongfeng touchpoint audit has no blockers.
- Real PM+01-05 model execution completed on `goal-test-daemon`:
  - issue: `1d1fa4ec-d7b7-4f41-b82a-b717e9b6740b` / `GOA-456`
  - model: `gpt-5.3-codex-spark`
  - fallback used: `false`
  - stages: PM, 01-clarify, 02-design, 03-task-split, 04-implement, 05-verify all completed with nonzero duration, messages, trace events, and token usage
  - latest runtime artifact: `artifacts/acceptance/goal-e-real-pm-0105-run-latest.json`
- Old failed/interrupted PM+01-05 attempts were removed from the live demo database; the retained issue set is:
  - `GOA-456` for true PM+01-05 model execution
  - `GOA-448` for the canonical trace/eval/optimizer/skill loop
- Full deployed UI acceptance passed after deployment:
  - `make goal-test-ui-acceptance`
  - latest training performance audit: `artifacts/acceptance/training-performance-audit-2026-06-26T07-23-10-598Z.json`
- Final deployment/log gates passed:
  - `node scripts/goal-test-environments.mjs verify int`
  - `node scripts/goal-test-environments.mjs verify-logs int`
- Final package and gap audit passed:
  - `node scripts/generate-tapd-gongfeng-sop-final-acceptance.mjs`
  - `node scripts/generate-goal-e-final-evidence-package.mjs`
  - `node scripts/tapd-gongfeng-sop-gap-audit.mjs`

## Still Open

None.

## Guardrail

Do not mark future Goal E work complete unless the live database, deployed web UI, final acceptance, final evidence package, gap audit, and post-browser log scan all pass against the current deployed commit.
