package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/verification"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 1. Exercise the original failed delivery through the owner-fenced API and persisted slot recovery.
func TestFailedDeliveryRepairWithoutProductionApproval(t *testing.T) {
	issue, plan := coordinationFixture(t)
	delivery, token := coordinationDelivery(t, issue, plan, "needs_changes")
	slotFixture(t, "verify-1", true)
	slotFixture(t, "verify-2", true)
	dbfx.Exec(t, `UPDATE issue_delivery SET error='first real failure',plan_snapshot=$2::jsonb WHERE id=$1`, delivery, `{"required_checks":[{"name":"real verification","stage":"verification"},{"name":"real integration","stage":"integration"}]}`)
	dbfx.Exec(t, `UPDATE verification_slot SET state='quarantined',workspace_id=$1,delivery_id=$2,worker_token=$3,source_commit=$4,candidate_id='',generation=29,lease_expires_at=now()-interval '1 minute' WHERE name='verify-1'`, testWorkspaceID, delivery, token, strings.Repeat("a", 40))
	dbfx.Exec(t, `INSERT INTO verification_slot_seed(name,seed_id,seed_sha256,verified) VALUES('verify-1','seed-reviewed','sha-reviewed',true)`)
	ownerToken, generation := claimReviewOwner(t, issue)
	revision := deliveryRevision(t, delivery)
	repair := deliveryRepairRequest{OperationID: dbid.NewV7().String(), Source: strings.Repeat("c", 40), Branch: "feature/repair", Base: strings.Repeat("b", 40), Environment: "verify-1", Generation: 29, SeedID: "seed-reviewed", SeedSHA: "sha-reviewed", Compatibility: "actual compatibility report"}
	evidence := []deliveryCheck{{Name: "compatibility and local checks", Status: "passed", Evidence: "reviewed accurate commit"}}
	request := func(b deliveryRepairRequest, rev int64, owner string) *http.Request {
		q := withURLParams(newRequest("POST", "/deliveries/action", map[string]any{"action": "retest", "revision": rev, "repair": b, "evidence": evidence}), "id", issue, "deliveryId", delivery)
		q.Header.Set("X-Delivery-Token", owner)
		q.Header.Set("X-Delivery-Generation", strconv.FormatInt(generation, 10))
		return q
	}
	for _, mutate := range []func(*deliveryRepairRequest){
		func(b *deliveryRepairRequest) { b.SeedSHA = "other-seed" },
		func(b *deliveryRepairRequest) { b.Environment = "verify-2" },
		func(b *deliveryRepairRequest) { b.Compatibility = "" },
	} {
		invalid := repair
		mutate(&invalid)
		testutil.Call(t, testHandler.ActIssueDelivery, request(invalid, revision, ownerToken)).Want(409)
	}
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='running-candidate' WHERE id=$1`, delivery)
	testutil.Call(t, testHandler.ActIssueDelivery, request(repair, revision, ownerToken)).Want(409)
	dbfx.Exec(t, `UPDATE issue_delivery SET candidate_id='' WHERE id=$1`, delivery)
	stale := repair
	stale.Generation = 28
	testutil.Call(t, testHandler.ActIssueDelivery, request(stale, revision, ownerToken)).Want(409)
	testutil.Call(t, testHandler.ActIssueDelivery, request(repair, revision-1, ownerToken)).Want(409)
	testutil.Call(t, testHandler.ActIssueDelivery, request(repair, revision, dbid.NewV7().String())).Want(409)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testutil.Call(t, testHandler.ActIssueDelivery, request(repair, revision, ownerToken)).Want(200)
		}()
	}
	wg.Wait()
	if got := deliveryRevision(t, delivery); got != revision+1 {
		t.Fatalf("duplicate repair changed revision: %d", got)
	}
	changed := repair
	changed.Source = strings.Repeat("d", 40)
	testutil.Call(t, testHandler.ActIssueDelivery, request(changed, revision, ownerToken)).Want(409)
	var phase, commit, failure string
	var unapproved bool
	dbfx.QueryRow(t, `SELECT phase,source_commit,approved_by IS NULL AND approved_at IS NULL,plan_snapshot->'verification_repair'->'previous'->>'error' FROM issue_delivery WHERE id=$1`, delivery).Scan(&phase, &commit, &unapproved, &failure)
	if phase != "waiting_int" || commit != repair.Source || !unapproved || failure != "first real failure" {
		t.Fatalf("repair lost identity or failure: %s %s %t %s", phase, commit, unapproved, failure)
	}
	if _, state := slotState(t, "verify-1"); state != "quarantined" {
		t.Fatal("request must not clear quarantine")
	}
	if _, state := slotState(t, "verify-2"); state != "idle" {
		t.Fatal("repair affected another slot")
	}
	old := verification.Lease{Name: "verify-1", Generation: 29, Workspace: testWorkspaceID, Delivery: delivery, Token: token, Source: strings.Repeat("a", 40)}
	store := verification.Store{DB: testHandler.TxStarter}
	if _, err := store.Repair(context.Background(), old, token, repair.Source, "artifact checked"); err == nil {
		t.Fatal("unclaimed delivery recovered")
	}
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "action": "claim"})).Want(200)
	var newToken string
	dbfx.QueryRow(t, `SELECT worker_token::text FROM issue_delivery WHERE id=$1`, delivery).Scan(&newToken)
	resumed, err := store.Repair(context.Background(), old, newToken, repair.Source, "exact artifact and unchanged baseline checked")
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Repair(context.Background(), old, newToken, repair.Source, "exact artifact and unchanged baseline checked")
	if err != nil || again.Generation != resumed.Generation {
		t.Fatalf("recovery replay changed lease: %+v %v", again, err)
	}
	if err := store.Advance(context.Background(), old, "heartbeat", ""); err == nil {
		t.Fatal("old worker survived recovery")
	}
	if resumed.Generation != 30 || resumed.State != "preparing" {
		t.Fatalf("unexpected lease: %+v", resumed)
	}
	// 2. A testing candidate cannot jump into production, and repair evidence remains separate from acceptance.
	testutil.Call(t, testHandler.AdvanceDelivery, newRequest("POST", "/advance", map[string]any{"id": delivery, "revision": deliveryRevision(t, delivery), "worker_token": newToken, "action": "publishing"})).Want(409)
	var snapshot json.RawMessage
	dbfx.QueryRow(t, `SELECT plan_snapshot FROM issue_delivery WHERE id=$1`, delivery).Scan(&snapshot)
	if !strings.Contains(string(snapshot), "real integration") {
		t.Fatal("required checks were lost")
	}
}
