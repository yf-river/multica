package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 只在显式原生验收入口运行，模型固定为本地确定性 provider。
func TestDshNativeInteractionSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("explicit native smoke only")
	}
	executable, home, module := os.Getenv("MULTICA_DSH_NATIVE_SMOKE_EXECUTABLE"), os.Getenv("MULTICA_DSH_NATIVE_SMOKE_HOME"), os.Getenv("MULTICA_DSH_HOST_LLM_MODULE")
	if executable == "" || home == "" || module == "" {
		t.Fatal("isolated native fixture paths required")
	}
	if _, err := os.Stat(filepath.Join(home, ".credentials.yaml")); !os.IsNotExist(err) {
		t.Fatal("native fixture home must not contain credentials")
	}
	work := t.TempDir()
	requests := filepath.Join(work, "requests.jsonl")
	run := func(phase, command, prompt, prior, decision string) Result {
		backend, err := New("dsh", Config{ExecutablePath: executable, TaskID: "native-adapter-" + phase, Env: map[string]string{
			"DSH_HOME": home, "DSH_TELEMETRY_DISABLED": "1", "MULTICA_DSH_PROFILE": "multica",
			"MULTICA_RUN_REAL_AGENT_SMOKE": "1", "MULTICA_DSH_HOST_LLM_MODULE": module,
			"MULTICA_DSH_FIXTURE_REQUESTS": requests, "MULTICA_DSH_FIXTURE_PHASE": phase,
		}})
		if err != nil {
			t.Fatal(err)
		}
		session, err := backend.Execute(context.Background(), prompt, ExecOptions{Cwd: work, Model: "multica-fixture/native-interaction", HumanInteractions: true, Command: command, ResumeSessionID: prior, PlanDecision: decision, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		t.Logf("phase=%s status=%s session=%s", phase, result.Status, result.SessionID)
		return result
	}
	plan := run("plan", "/plan", "Plan a write to approved.txt. Wait for approval.", "", "")
	if plan.Status != "waiting_plan" || plan.PendingInteraction == nil || plan.PendingInteraction.PlanMarkdown == "" {
		t.Fatalf("plan not suspended: %#v", plan)
	}
	if _, err := os.Stat(filepath.Join(work, "approved.txt")); !os.IsNotExist(err) {
		t.Fatal("plan wrote before approval")
	}
	keep := run("keep-planning", "/plan", "继续规划，暂不执行。补充回滚步骤。", plan.SessionID, "keep_planning")
	if keep.Status != "waiting_plan" || keep.PendingInteraction == nil || keep.SessionID != plan.SessionID {
		t.Fatalf("keep planning lost session: %#v", keep)
	}
	if _, err := os.Stat(filepath.Join(work, "approved.txt")); !os.IsNotExist(err) {
		t.Fatal("keep planning wrote before approval")
	}
	approved := run("approved", "/plan off", "已批准本轮计划，请按计划执行。", keep.SessionID, "approved")
	if approved.Status != "completed" || approved.SessionID != plan.SessionID {
		t.Fatalf("approval did not resume: %#v", approved)
	}
	data, err := os.ReadFile(filepath.Join(work, "approved.txt"))
	if err != nil || string(data) != "DSH_APPROVED_WRITE" {
		t.Fatalf("native write failed: %q %v", data, err)
	}
}
