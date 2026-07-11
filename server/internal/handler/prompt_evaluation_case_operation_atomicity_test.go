package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
)

type promptEvaluationCaseOperationFixture struct {
	assetID string
	caseID  string
}

func newPromptEvaluationCaseOperationFixture(t *testing.T) promptEvaluationCaseOperationFixture {
	t.Helper()
	ctx := context.Background()
	name := "durable bulk tags " + uuid.NewString()
	var assetID, caseID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, name, asset_type, payload, created_by)
		VALUES ($1, $2, '数据集', '{"cases":[]}'::jsonb, $3) RETURNING id
	`, testWorkspaceID, name, testUserID).Scan(&assetID); err != nil {
		t.Fatalf("create bulk-tag asset: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_case (
			workspace_id, asset_id, case_name, tags, status, source, created_by
		) VALUES ($1, $2, 'durable case', '["before"]'::jsonb, '启用', 'manual', $3)
		RETURNING id
	`, testWorkspaceID, assetID, testUserID).Scan(&caseID); err != nil {
		t.Fatalf("create bulk-tag case: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `
			DELETE FROM domain_event_outbox
			WHERE event_type = $1
			  AND payload->>'operation_id' IN (
				SELECT id::text FROM prompt_evaluation_case_operation WHERE asset_id = $2
			  )
		`, promptEvaluationCaseOperationRequestedEvent, assetID)
		mustExec(t, context.Background(), `DELETE FROM prompt_evaluation_asset WHERE id = $1`, assetID)
	})
	return promptEvaluationCaseOperationFixture{assetID: assetID, caseID: caseID}
}

func queuePromptEvaluationCaseOperation(t *testing.T, fixture promptEvaluationCaseOperationFixture) (*httptest.ResponseRecorder, PromptEvaluationCaseOperationResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.BulkUpdatePromptEvaluationCaseTags(w, newRequest(http.MethodPost, "/api/prompt-evaluation-cases/bulk-tags", map[string]any{
		"asset_id":       fixture.assetID,
		"source":         "manual",
		"tag":            "before",
		"tags":           []string{"after"},
		"mode":           "追加",
		"execution_mode": "后台",
		"limit":          50,
	}))
	var response struct {
		Operation PromptEvaluationCaseOperationResponse `json:"operation"`
	}
	if w.Code == http.StatusAccepted {
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode queued bulk-tag operation: %v", err)
		}
	}
	return w, response.Operation
}

func consumePromptEvaluationCaseOperationForTest(operationID string) error {
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := testHandler.consumePromptEvaluationCaseOperation(ctx, testHandler.Queries.WithTx(tx), events.Event{
		Type:        promptEvaluationCaseOperationRequestedEvent,
		WorkspaceID: testWorkspaceID,
		Payload:     map[string]any{"operation_id": operationID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestQueuePromptEvaluationCaseOperationRollsBackWithoutOutboxEvent(t *testing.T) {
	fixture := newPromptEvaluationCaseOperationFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_case_operation_event_failure_" + suffix
	triggerName := "test_case_operation_event_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.event_type = '%s' THEN
				RAISE EXCEPTION 'forced case operation event failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE INSERT ON domain_event_outbox
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, promptEvaluationCaseOperationRequestedEvent, triggerName, functionName)); err != nil {
		t.Fatalf("install case operation event failure: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON domain_event_outbox`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	w, _ := queuePromptEvaluationCaseOperation(t, fixture)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var operations int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM prompt_evaluation_case_operation WHERE asset_id = $1`, fixture.assetID).Scan(&operations); err != nil {
		t.Fatalf("count queued operations: %v", err)
	}
	if operations != 0 {
		t.Fatalf("operation survived missing outbox event: %d", operations)
	}
}

func TestPromptEvaluationCaseOperationConsumerRetriesAtomically(t *testing.T) {
	fixture := newPromptEvaluationCaseOperationFixture(t)
	w, operation := queuePromptEvaluationCaseOperation(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("queue operation: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_case_operation_update_failure_" + suffix
	triggerName := "test_case_operation_update_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = '%s'::uuid THEN
				RAISE EXCEPTION 'forced case operation update failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON prompt_evaluation_case
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, fixture.caseID, triggerName, functionName)); err != nil {
		t.Fatalf("install case operation update failure: %v", err)
	}
	dropFailure := func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_case`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	}
	t.Cleanup(dropFailure)

	if err := consumePromptEvaluationCaseOperationForTest(operation.ID); err == nil {
		t.Fatal("expected first consumer attempt to fail")
	}
	var status string
	var tags []byte
	if err := testPool.QueryRow(ctx, `SELECT status FROM prompt_evaluation_case_operation WHERE id = $1`, operation.ID).Scan(&status); err != nil {
		t.Fatalf("load operation after rollback: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT tags FROM prompt_evaluation_case WHERE id = $1`, fixture.caseID).Scan(&tags); err != nil {
		t.Fatalf("load case tags after rollback: %v", err)
	}
	if status != "已入队" || strings.Contains(string(tags), "after") {
		t.Fatalf("failed consumer left partial state: status=%q tags=%s", status, tags)
	}

	dropFailure()
	if err := consumePromptEvaluationCaseOperationForTest(operation.ID); err != nil {
		t.Fatalf("retry consumer: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM prompt_evaluation_case_operation WHERE id = $1`, operation.ID).Scan(&status); err != nil {
		t.Fatalf("load completed operation: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT tags FROM prompt_evaluation_case WHERE id = $1`, fixture.caseID).Scan(&tags); err != nil {
		t.Fatalf("load case tags after retry: %v", err)
	}
	if status != "已完成" || !strings.Contains(string(tags), "after") {
		t.Fatalf("retry did not commit complete state: status=%q tags=%s", status, tags)
	}
}

func TestPromptEvaluationCaseOperationConcurrentConsumersConverge(t *testing.T) {
	fixture := newPromptEvaluationCaseOperationFixture(t)
	w, operation := queuePromptEvaluationCaseOperation(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("queue operation: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- consumePromptEvaluationCaseOperationForTest(operation.ID)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent consumer: %v", err)
		}
	}

	ctx := context.Background()
	var status string
	var tags []byte
	if err := testPool.QueryRow(ctx, `SELECT status FROM prompt_evaluation_case_operation WHERE id = $1`, operation.ID).Scan(&status); err != nil {
		t.Fatalf("load concurrent operation: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT tags FROM prompt_evaluation_case WHERE id = $1`, fixture.caseID).Scan(&tags); err != nil {
		t.Fatalf("load concurrent case tags: %v", err)
	}
	if status != "已完成" || strings.Count(string(tags), "after") != 1 {
		t.Fatalf("concurrent consumers diverged: status=%q tags=%s", status, tags)
	}
}

func TestPromptEvaluationCaseOperationDeadLetterSetsTruthfulFailure(t *testing.T) {
	fixture := newPromptEvaluationCaseOperationFixture(t)
	w, operation := queuePromptEvaluationCaseOperation(t, fixture)
	if w.Code != http.StatusAccepted {
		t.Fatalf("queue operation: expected 202, got %d: %s", w.Code, w.Body.String())
	}

	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_case_operation_dead_letter_" + suffix
	triggerName := "test_case_operation_dead_letter_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.id = '%s'::uuid THEN
				RAISE EXCEPTION 'forced terminal case operation failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE UPDATE ON prompt_evaluation_case
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, fixture.caseID, triggerName, functionName)); err != nil {
		t.Fatalf("install terminal case operation failure: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON prompt_evaluation_case`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})

	dispatcher, err := eventoutbox.NewDispatcher(
		testHandler.Queries,
		testPool,
		events.New(),
		"case-operation-dead-letter-"+uuid.NewString(),
		eventoutbox.DispatcherConfig{
			BatchSize:   10,
			Lease:       time.Second,
			RetryBase:   time.Millisecond,
			MaxRetry:    time.Millisecond,
			MaxAttempts: 1,
		},
	)
	if err != nil {
		t.Fatalf("create case operation dispatcher: %v", err)
	}
	mustExec(t, ctx, `
		DELETE FROM domain_event_outbox event
		WHERE event.event_type = $1
		  AND event.workspace_id = $2
		  AND NOT EXISTS (
			SELECT 1 FROM prompt_evaluation_case_operation operation
			WHERE operation.id::text = event.payload->>'operation_id'
		  )
	`, promptEvaluationCaseOperationRequestedEvent, testWorkspaceID)
	if err := testHandler.RegisterPromptEvaluationCaseOperationConsumer(dispatcher); err != nil {
		t.Fatalf("register case operation consumer: %v", err)
	}
	if count, err := dispatcher.ProcessBatch(ctx); count != 1 || err == nil {
		t.Fatalf("dead-letter batch = (%d, %v), want one terminal consumer failure", count, err)
	}

	var status, errorMessage string
	var deadLettered bool
	if err := testPool.QueryRow(ctx, `
		SELECT status, error_message FROM prompt_evaluation_case_operation WHERE id = $1
	`, operation.ID).Scan(&status, &errorMessage); err != nil {
		t.Fatalf("load dead-lettered operation: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT dead_lettered_at IS NOT NULL
		FROM domain_event_outbox
		WHERE event_type = $1 AND payload->>'operation_id' = $2
	`, promptEvaluationCaseOperationRequestedEvent, operation.ID).Scan(&deadLettered); err != nil {
		t.Fatalf("load dead-lettered operation event: %v", err)
	}
	if status != "失败" || errorMessage == "" || !deadLettered {
		t.Fatalf("dead-letter state drifted: status=%q error=%q event_dead=%v", status, errorMessage, deadLettered)
	}
}
