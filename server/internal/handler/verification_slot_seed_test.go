package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// slotFixture 建立空闲槽位；用例结束后连同种子身份和维护审计一起清理。
func slotFixture(t *testing.T, name string, enabled bool) {
	t.Helper()
	dbfx.Cleanup(t, `DELETE FROM verification_slot_seed WHERE name=$1`, name)
	dbfx.Cleanup(t, `DELETE FROM verification_slot_maintenance WHERE name=$1`, name)
	dbfx.Cleanup(t, `DELETE FROM verification_slot WHERE name=$1`, name)
	dbfx.Exec(t, `INSERT INTO verification_slot(name,enabled,state) VALUES($1,$2,'idle') ON CONFLICT(name) DO UPDATE SET enabled=EXCLUDED.enabled,state='idle',workspace_id=NULL,delivery_id=NULL,worker_token=NULL,lease_expires_at=NULL,error=''`, name, enabled)
}

func slotState(t *testing.T, name string) (bool, string) {
	t.Helper()
	var enabled bool
	var state string
	dbfx.QueryRow(t, `SELECT enabled,state FROM verification_slot WHERE name=$1`, name).Scan(&enabled, &state)
	return enabled, state
}

func callSlot(t *testing.T, fn http.HandlerFunc, body map[string]any) *testutil.Response {
	t.Helper()
	return testutil.Call(t, fn, newRequest("POST", "/api/delivery-queue/slots", body))
}

func callSlotSeed(t *testing.T, name string) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.GetVerificationSlotSeed, newRequest("GET", "/api/delivery-queue/slots/seed?name="+name, nil))
}

func callSlots(t *testing.T) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.GetVerificationLease, newRequest("GET", "/api/delivery-queue/slots", nil))
}

func TestVerificationSlotMaintenanceStopsOnlyItself(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	slotFixture(t, "verify-1", true)
	slotFixture(t, "verify-2", true)
	var maintained map[string]any
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-1", "action": "maintain", "reason": "按种子恢复测试数据"}).Want(200).JSON(&maintained)
	if maintained["previous_enabled"] != true {
		t.Fatalf("未返回原启用状态: %v", maintained)
	}
	if enabled, _ := slotState(t, "verify-1"); enabled {
		t.Fatal("维护期间槽位仍在接单")
	}
	if enabled, _ := slotState(t, "verify-2"); !enabled {
		t.Fatal("维护一个槽位影响了另一个槽位")
	}
	var audits int
	dbfx.QueryRow(t, `SELECT count(*) FROM verification_slot_maintenance WHERE name='verify-1' AND action='maintain'`).Scan(&audits)
	if audits == 0 {
		t.Fatal("缺少维护审计")
	}
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-1", "action": "resume-maintenance", "reason": "恢复完成"}).Want(200)
	if enabled, _ := slotState(t, "verify-1"); !enabled {
		t.Fatal("维护结束后没有恢复接单")
	}
	var resumes int
	dbfx.QueryRow(t, `SELECT count(*) FROM verification_slot_maintenance WHERE name='verify-1' AND action='resume'`).Scan(&resumes)
	if resumes == 0 {
		t.Fatal("缺少恢复审计")
	}
}

func TestVerificationMaintenanceReportsPreviouslyDisabledSlot(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	slotFixture(t, "verify-1", false)
	var state map[string]any
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-1", "action": "maintain", "reason": "测试种子"}).Want(200).JSON(&state)
	if state["previous_enabled"] != false || state["enabled"] != false {
		t.Fatalf("禁用槽位的维护状态错误: %v", state)
	}
}

func TestVerificationSlotMaintenanceRefusesBusySlot(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	slotFixture(t, "verify-3", true)
	delivery := dbid.NewV7().String()
	token := dbid.NewV7().String()
	dbfx.Exec(t, `UPDATE verification_slot SET state='verifying',delivery_id=$2,worker_token=$3,source_commit=$4,lease_expires_at=now()+interval '2 minutes' WHERE name=$1`, "verify-3", delivery, token, strings.Repeat("a", 40))
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-3", "action": "maintain", "reason": "正在验收"}).Want(409)
	if enabled, state := slotState(t, "verify-3"); !enabled || state != "verifying" {
		t.Fatalf("活动验收被维护打断: enabled=%v state=%s", enabled, state)
	}
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-3", "action": "resume-maintenance", "reason": "强制恢复"}).Want(409)
}

func TestVerificationSlotSeedRequiresMaintenanceAndRecordsVersion(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	slotFixture(t, "verify-2", true)
	sha := strings.Repeat("ab", 32)
	callSlot(t, testHandler.RecordVerificationSlotSeed, map[string]any{"name": "verify-2", "seed_id": "seed-a", "seed_sha256": sha, "verified": true}).Want(409)
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-2", "action": "maintain", "reason": "登记种子"}).Want(200)
	callSlot(t, testHandler.RecordVerificationSlotSeed, map[string]any{"name": "verify-2", "seed_id": "seed-a", "seed_sha256": sha, "verified": true}).Want(200)
	callSlot(t, testHandler.RecordVerificationSlotSeed, map[string]any{"name": "verify-2", "seed_id": "seed-a", "seed_sha256": "not-a-checksum", "verified": true}).Want(400)
	response := callSlotSeed(t, "verify-2").Want(200)
	var seed map[string]any
	response.JSON(&seed)
	if seed["seed_id"] != "seed-a" || seed["verified"] != true || seed["seed_sha256"] != sha {
		t.Fatalf("种子身份记录不准确: %v", seed)
	}
}

func TestVerificationSlotListExposesSeedIdentity(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	slotFixture(t, "verify-1", true)
	sha := strings.Repeat("cd", 32)
	callSlot(t, testHandler.ConfigureVerificationSlot, map[string]any{"name": "verify-1", "action": "maintain", "reason": "登记种子"}).Want(200)
	callSlot(t, testHandler.RecordVerificationSlotSeed, map[string]any{"name": "verify-1", "seed_id": "seed-b", "seed_sha256": sha, "verified": false}).Want(200)
	response := callSlots(t).Want(200)
	var slots []map[string]any
	response.JSON(&slots)
	found := false
	for _, item := range slots {
		if item["name"] != "verify-1" {
			continue
		}
		found = true
		if item["seed_id"] != "seed-b" || item["seed_verified"] != false || item["enabled"] != false {
			t.Fatalf("槽位清单没有反映种子身份或维护状态: %v", item)
		}
	}
	if !found {
		t.Fatal("槽位清单缺少目标槽位")
	}
}
