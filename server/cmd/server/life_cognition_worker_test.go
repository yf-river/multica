package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestLifeTextExcerptPreservesShortTextAndBoundsLongText(t *testing.T) {
	if got := lifeTextExcerpt("搭子", 4); got != "搭子" {
		t.Fatalf("short excerpt=%q", got)
	}
	if got := lifeTextExcerpt("一二三四五", 4); got != "一二三四…" {
		t.Fatalf("bounded excerpt=%q", got)
	}
}

func TestPGIntervalDurationUsesSafeFallback(t *testing.T) {
	if got := pgIntervalDuration(pgtype.Interval{}, 9*time.Hour); got != 9*time.Hour {
		t.Fatalf("invalid interval fallback=%s", got)
	}
	if got := pgIntervalDuration(pgtype.Interval{Days: 2, Valid: true}, time.Hour); got != 48*time.Hour {
		t.Fatalf("two days=%s", got)
	}
}

func TestRunningLifeCognitionJobPersistsGovernedInput(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	var agentID, runtimeID, jobID, previousTaskID pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT id,runtime_id FROM agent WHERE workspace_id=$1 ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO life_cognition_job (
			workspace_id,user_id,companion_agent_id,job_type,dedupe_key,input,status,started_at,attempt
		) VALUES ($1,$2,$3,'understand_materials',$4,'{}'::jsonb,'running',now(),1)
		RETURNING id
	`, testWorkspaceID, testUserID, agentID, "persist-input:"+fmt.Sprint(time.Now().UnixNano())).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id,runtime_id,status,context,initiator_user_id)
		VALUES ($1,$2,'failed','{}'::jsonb,$3)
		RETURNING id
	`, agentID, runtimeID, testUserID).Scan(&previousTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE life_cognition_job SET task_id=$2 WHERE id=$1`, jobID, previousTaskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM life_cognition_job WHERE id=$1`, jobID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, previousTaskID)
	})

	input := json.RawMessage(`{"context_version":"life-context-v5","processing_cursor":"cursor-1","new_materials":[{"id":"source-1"}]}`)
	queries := db.New(testPool)
	if err := queries.UpdateRunningLifeCognitionJobInput(ctx, db.UpdateRunningLifeCognitionJobInputParams{ID: jobID, Input: input}); err != nil {
		t.Fatal(err)
	}
	var persisted json.RawMessage
	if err := testPool.QueryRow(ctx, `SELECT input FROM life_cognition_job WHERE id=$1`, jobID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	var persistedObject map[string]any
	if err := json.Unmarshal(persisted, &persistedObject); err != nil {
		t.Fatal(err)
	}
	if persistedObject["context_version"] != lifeCognitionContextVersion || persistedObject["processing_cursor"] != "cursor-1" {
		t.Fatalf("persisted input mismatch: %s", persisted)
	}
}

func TestClaimDueLifeCognitionJobsSerializesMaterialUnderstandingPerLife(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `LOCK TABLE life_cognition_job IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE life_cognition_job
		SET scheduled_at = now() + interval '1 hour'
		WHERE status IN ('queued', 'failed')
	`); err != nil {
		t.Fatal(err)
	}

	var agentID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id=$1 ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	insertJob := func(jobType, status, key string) pgtype.UUID {
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO life_cognition_job (
				workspace_id,user_id,companion_agent_id,job_type,status,dedupe_key,input,scheduled_at,started_at,attempt
			) VALUES ($1,$2,$3,$4,$5,$6,'{}'::jsonb,now()-interval '1 day',CASE WHEN $5='running' THEN now() END,CASE WHEN $5='running' THEN 1 ELSE 0 END)
			RETURNING id
		`, testWorkspaceID, testUserID, agentID, jobType, status, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	runningID := insertJob("understand_materials", "running", "serial-running:"+suffix)
	queuedID := insertJob("understand_materials", "queued", "serial-queued:"+suffix)
	chronicleID := insertJob("chronicle_generate", "queued", "serial-chronicle:"+suffix)
	secondQueuedID := insertJob("understand_materials", "queued", "serial-second-queued:"+suffix)
	queries := db.New(tx)

	claimed, err := queries.ClaimDueLifeCognitionJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != chronicleID {
		t.Fatalf("claim with running understanding = %#v, want only chronicle %v", claimed, chronicleID)
	}
	if _, err := tx.Exec(ctx, `UPDATE life_cognition_job SET status='completed',completed_at=now() WHERE id=$1`, runningID); err != nil {
		t.Fatal(err)
	}

	claimed, err = queries.ClaimDueLifeCognitionJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || (claimed[0].ID != queuedID && claimed[0].ID != secondQueuedID) {
		t.Fatalf("claim after understanding completed = %#v, want one queued material understanding", claimed)
	}
	remainingID := queuedID
	if claimed[0].ID == queuedID {
		remainingID = secondQueuedID
	}
	var secondStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM life_cognition_job WHERE id=$1`, remainingID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if secondStatus != "queued" {
		t.Fatalf("second material understanding status = %s, want queued", secondStatus)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT concurrent_understanding`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE life_cognition_job SET status='running' WHERE id=$1`, remainingID); err == nil {
		t.Fatal("database allowed two running material-understanding jobs for one life")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT concurrent_understanding`); err != nil {
		t.Fatal(err)
	}
}

func TestMissingLifeChroniclePeriodsCatchUpInDependencyOrder(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID, workspaceID pgtype.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, account) VALUES ('Chronicle Catchup', $1) RETURNING id`, "chronicle-catchup-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("create catchup user: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Chronicle Catchup', $1, 'CHR') RETURNING id`, "chronicle-catchup-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("create catchup workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	for index, occurredAt := range []time.Time{
		time.Date(2020, 12, 31, 10, 0, 0, 0, time.UTC),
		time.Date(2021, 1, 1, 10, 0, 0, 0, time.UTC),
	} {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO life_material (workspace_id,user_id,source_type,source_key,source_revision,content,occurred_at)
			VALUES ($1,$2,'manual',$3,'1',$4,$5)
		`, workspaceID, userID, fmt.Sprintf("catchup-%s-%d", suffix, index), "跨年材料", occurredAt); err != nil {
			t.Fatalf("create catchup material: %v", err)
		}
	}
	queries := db.New(testPool)
	list := func() []db.ListMissingLifeChroniclePeriodsRow {
		periods, err := queries.ListMissingLifeChroniclePeriods(ctx, db.ListMissingLifeChroniclePeriodsParams{
			WorkspaceID: workspaceID, UserID: userID, MaxPeriods: 32,
			BeforeTime: pgtype.Timestamptz{Time: time.Date(2021, 1, 3, 0, 0, 0, 0, time.UTC), Valid: true},
		})
		if err != nil {
			t.Fatalf("list missing periods: %v", err)
		}
		return periods
	}
	periods := list()
	if len(periods) != 2 || periods[0].PeriodKind != "day" || periods[1].PeriodKind != "day" {
		t.Fatalf("first catchup must contain the two material days: %#v", periods)
	}
	for _, period := range periods {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO life_chronicle_entry (
				workspace_id,user_id,period_start,period_end,facts,period_kind,status,generated_by
			) VALUES ($1,$2,$3,$4,'日记录',$5,'published','companion')
		`, workspaceID, userID, period.PeriodStart, period.PeriodEnd, period.PeriodKind); err != nil {
			t.Fatalf("create daily chronicle: %v", err)
		}
	}
	periods = list()
	if len(periods) == 0 || periods[0].PeriodKind != "month" || periods[0].PeriodStart.Time.Format("2006-01-02") != "2020-12-01" {
		t.Fatalf("month did not become ready after daily catchup: %#v", periods)
	}
	month := periods[0]
	if _, err := testPool.Exec(ctx, `
		INSERT INTO life_chronicle_entry (
			workspace_id,user_id,period_start,period_end,facts,period_kind,status,generated_by
		) VALUES ($1,$2,$3,$4,'月记录','month','published','companion')
	`, workspaceID, userID, month.PeriodStart, month.PeriodEnd); err != nil {
		t.Fatalf("create monthly chronicle: %v", err)
	}
	periods = list()
	foundYear := false
	for _, period := range periods {
		if period.PeriodKind == "year" && period.PeriodStart.Time.Year() == 2020 {
			foundYear = true
		}
	}
	if !foundYear {
		t.Fatalf("year did not become ready after monthly catchup: %#v", periods)
	}
}
