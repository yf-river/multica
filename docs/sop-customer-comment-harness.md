# Customer-Comment SOP Harness Notes

This document captures reusable harness rules learned from the TAPD to issue
comment-driven SOP acceptance run. These rules belong to `goal-test` harnesses
and shared SOP acceptance materials, not to project-level operation skills in
`user-center`, `gateway`, or `ida-deployment`.

## User-Journey Contract

- Create a unique fixture through public UI/API entrypoints.
- Drive manual decisions through issue comments: confirm, return to clarify,
  return to implementation, continue verification, CodeReview feedback, and
  incremental requirements.
- Validate both paths:
  - complex tasks pause for comments at risky boundaries;
  - simple low-risk tasks can run through 01-05 automatically.
- Keep all generated evidence attached to the issue or run review: source cards,
  markdown artifacts, task traces, MR links, review comments, and exported CSV.

## Cascade Checks

Every user action in the harness must assert the expected platform side effects:

- task count, task status, attempt count, and leader/follower role;
- issue status, parent/child links, project ownership, and assignee type;
- comments, trigger comment ids, source fetch events, trace events, and inbox
  notifications;
- artifact registration, download URLs, preview content, run review links, and
  export files;
- absence of duplicate PM handoff tasks, ghost stages, stale review nodes, or
  hidden blocked tasks.

Do not accept a final `ok=true` unless these intermediate side effects match
the contract.

## Runtime Preflight

Any harness that starts an Agent task must verify runtime capability before the
long run:

- Codex model catalog and Responses smoke;
- Gongfeng and TAPD credential/profile availability;
- MCP server availability and source-fetch/writeback paths;
- proxy configuration and `CODEX_HOME` isolation;
- model tool capability, especially image generation support;
- logs for transport failures, poisoned sessions, token renewal noise, and
  app-server restart churn.

Runtime fixes should be service/profile/broker level. Do not treat a user's
shell rc file, personal proxy export, or one-off node switch as a portable fix.

## Evidence Discipline

- Final artifacts must include a requirement matrix with
  `fulfilled`, `partial`, `missing`, `blocked`, or `out_of_scope_by_user`.
- Browser-level checks should prove source cards, attachments, previews,
  timeline, tooltips, thinking rounds, and CSV exports.
- Keep raw artifacts under `artifacts/acceptance/`; the directory is ignored by
  git and can remain in the project for local review.
- Archive only when preserving a completed run across cleanup or database reset.

## Skill Boundary

Promote only project-specific, frequent, fragile, and verifiable actions to
project operation skills. Keep these platform-level capabilities in goal-test
harnesses or shared SOP skills:

- historical benchmark replay orchestration;
- customer-comment SOP flow;
- Runtime Broker/network recovery;
- final acceptance matrix generation;
- run review/export validation;
- generic commit/fix-bug workflows.
