# Prompt Evaluation flow

## Model ownership

Prompt Library items and versions are authored prompts. Evaluation Assets own
the executable evaluation profile and dataset association. Structured Cases
are the current editable case library; Dataset Versions snapshot rows for
reproducible comparison. Runs own one execution attempt and Trials own the
per-case result. Evidence Snapshots and Optimization Candidates are derived
review artifacts, not alternate Run state.

The Prompt Library page delegates general writes, cache invalidation and user
feedback to `usePromptLibraryMutations`. Skill-candidate freshness/apply/re-eval
actions have their own controller hook. Presentation panels do not call those
mutation APIs or maintain a second cache policy.

## Run lifecycle

`POST /api/prompt-evaluation-assets/{id}/run` validates the workspace-scoped
Asset, runtime/Agent choice and current Cases or locked Dataset Version before
creating Run/Trials and the Agent Task. The task identity on the Run is the
join between the general execution system and evaluation state.

Task completion, failure and cancellation first commit the terminal Task and
durable domain event. The `prompt_evaluation_projection` consumer then runs
`ProjectPromptEvaluationTerminalTask` in its receipt transaction:

- a retry child replaces the failed parent as the Run's authoritative task;
- usage, messages and structured per-case verdicts determine Run status,
  metrics, conclusion and evidence;
- Trials, dimension scores and the Asset's latest Agent-run snapshot update in
  the same consumer transaction;
- missing machine-readable verdicts become `需人工复核`, never fabricated
  pass results.

Manual `/sync` invokes the same service projection against the Run's bound
Task; it is a repair entry point, not a second scoring implementation. Review,
evidence snapshot and candidate publication operate on the persisted Run.

## Dataset and candidate boundaries

Dataset import/version/restore remains workspace- and Asset-scoped. A Version
is immutable evidence for a comparison; restore creates current editable state
from that snapshot rather than mutating historical rows.

Bulk Case tag operations have one synchronous executor and one durable
background delivery path. A background request commits the Operation row and
its `prompt_evaluation_case_operation:requested` outbox event together. The
consumer applies Case tags, synchronized Dataset/Test Suite rows, Asset payload
and Operation completion in its receipt transaction. Transient failures roll
the whole projection back for retry. When retries are exhausted, the generic
outbox dead-letter hook marks both the event and Operation failed in one
transaction, so the UI never remains indefinitely at `已入队` after terminal
failure. Requeueing the dead letter resumes the same stored filter/input rather
than rebuilding intent from current UI state.

Skill candidates preserve their source snapshot/freshness evidence. Applying a
candidate and preparing/running re-evaluation are explicit endpoints with one
controller-owned UI flow. Repository writes remain an external boundary and
must return visible errors; candidate status is not treated as proof that an
unverified repository write succeeded.

## Verification anchors

- Asset/run creation: `server/internal/handler/prompt_evaluation_asset.go`
- Cases and versions: `server/internal/handler/prompt_evaluation_cases.go` and
  `server/internal/handler/prompt_evaluation_dataset_versions.go`
- Durable bulk Case operations:
  `server/internal/handler/prompt_evaluation_case_operation_consumer.go` and
  `server/internal/eventoutbox/outbox.go`
- Run/evidence reads: `server/internal/handler/prompt_evaluation_runs.go`
- Terminal projection: `server/internal/service/prompt_evaluation_sync.go` and
  `server/cmd/server/prompt_evaluation_projection.go`
- Candidate/Skill boundary: `server/internal/handler/prompt_evaluation_skill.go`
- Frontend mutation ownership:
  `packages/views/prompt-library/components/use-prompt-library-mutations.ts` and
  `packages/views/prompt-library/components/use-skill-candidate-workflow-actions.ts`
