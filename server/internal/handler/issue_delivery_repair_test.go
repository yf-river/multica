package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	resumed := largeInventoryWorkerRepair(t, old, newToken, repair.Source)
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

// Modification 1. Send a real worker's 6147-file repair through the unchanged handler and retain the oversized-request regression.
func largeInventoryWorkerRepair(t *testing.T, old verification.Lease, token, source string) verification.Lease {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		testHandler.AdvanceVerificationSlot(w, req)
	}))
	defer server.Close()
	script, err := filepath.Abs("../../../scripts/issue-delivery-worker.py")
	if err != nil {
		t.Fatal(err)
	}
	lease, _ := json.Marshal(old)
	code := `import importlib.util,json,sys,tempfile,urllib.error
from pathlib import Path
from unittest.mock import patch,Mock
spec=importlib.util.spec_from_file_location('worker',sys.argv[1]);w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r
old=json.loads(sys.argv[3]);token=sys.argv[4];source=sys.argv[5]
worker=object.__new__(w.Worker);worker.env='int';worker.meta=dict(backend_url=sys.argv[2]);worker.profile=dict(token='fixture',workspace_id='fixture')
with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
 artifact=Path(folder)/'artifact';artifact.mkdir()
 for i in range(6147):(artifact/(str(i)+'x'*80)).write_text('verified content')
 manifest=dict(id='candidate',source_commit=source,source_digest='fixture',files=r.checksums(artifact))
 (artifact/'release.json').write_text(json.dumps(manifest))
 body=dict(old,action='repair',replacement_worker_token=token,replacement_source_commit=source,detail=json.dumps(dict(files=manifest['files'])))
 # Reproduce the original wire protocol directly, before the worker's new preflight bound.
 request=w.urllib.request.Request(sys.argv[2]+'/api/delivery-queue/slots/advance',data=json.dumps(body).encode(),headers={'Content-Type':'application/json'})
 try:w.urllib.request.urlopen(request);raise AssertionError('oversize request accepted')
 except urllib.error.HTTPError as error:assert error.code==400
 metadata=dict(release_dir='/old',release_id='old-release')
 original_read=r.read
 with patch.object(r,'read',side_effect=lambda path:original_read(path) if str(path).endswith('release.json') else metadata),patch.object(r,'check_process_ownership'),patch.object(worker,'seed_matches',return_value=True),patch.object(worker,'source',return_value=Path(folder)),patch.object(r,'build_verification',return_value=artifact),patch.object(r,'validate_pending_migrations'),patch.object(r,'candidate_path',return_value=Path(folder)/'missing'):
  d=dict(id=old['delivery_id'],source_commit=source,worker_token=token,plan_snapshot=dict(verification_repair=dict(request=dict(environment=old['name'],generation=old['generation'],compatibility_evidence='report'*20000))))
  result=worker.prepare_repair(d,dict(lease=old),Mock())
  print(json.dumps(result[2]))
`
	cmd := exec.Command("python3", "-c", code, script, server.URL, string(lease), token, source)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worker to handler: %v: %s", err, output)
	}
	var result verification.Lease
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("worker response: %v: %s", err, output)
	}
	return result
}
