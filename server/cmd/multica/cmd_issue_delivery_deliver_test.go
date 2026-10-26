package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	dir := t.TempDir()
	for _, path := range []string{"server/cmd/server", "apps/web"} {
		if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "feature/deliver"},
		{"config", "user.email", "deliver@test.local"},
		{"config", "user.name", "deliver"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(head))
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type recordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

// deliveryServer fakes the delivery API and records the order of calls so the
// test can assert the CLI performs the documented sequence, not just succeeds.
func deliveryServer(t *testing.T, issueID string) (*httptest.Server, *[]recordedRequest, func() int) {
	t.Helper()
	var (
		mu       sync.Mutex
		requests []recordedRequest
	)
	deliveries := 0
	reads := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/delivery-owner"):
			_, _ = w.Write([]byte(`{"token":"tok","generation":1}`))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`{"id":"22222222-2222-4222-8222-222222222222","revision":3}`))
		case strings.HasSuffix(r.URL.Path, "/delivery-plan/confirm"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/delivery-plan"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/deliveries") && r.Method == http.MethodGet:
			mu.Lock()
			reads++
			current := reads
			mu.Unlock()
			phase := "waiting_int"
			candidate := ""
			if current > 1 {
				phase = "testing"
			}
			if current > 2 {
				candidate = "55555555-5555-4555-8555-555555555555"
			}
			_, _ = w.Write([]byte(`{"plan":{"id":"33333333-3333-4333-8333-333333333333","comment_revision":3},"deliveries":[{"id":"44444444-4444-4444-8444-444444444444","plan_id":"33333333-3333-4333-8333-333333333333","phase":"` + phase + `","source_commit":"` + issueID + `","revision":2,"candidate_id":"` + candidate + `"}]}`))
		case strings.HasSuffix(r.URL.Path, "/deliveries"):
			mu.Lock()
			deliveries++
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(requests)
	}
	return server, &requests, count
}

func setCLIClientEnv(t *testing.T, baseURL string) {
	t.Helper()
	for _, key := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_DAEMON_PORT"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", baseURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace")
	t.Setenv("MULTICA_TOKEN", "test-token")
}

func TestDeliveryDeliverRegistersInOrder(t *testing.T) {
	repo, head := gitRepo(t)
	issueID := "11111111-1111-4111-8111-111111111111"
	server, requests, _ := deliveryServer(t, head)
	setCLIClientEnv(t, server.URL)

	plan := writeTemp(t, "plan.md", "## 方案\n\n完整方案正文。\n")
	checks := writeTemp(t, "checks.json", `[{"name":"业务功能","stage":"verification"},{"name":"组合验收","stage":"integration"}]`)
	evidence := writeTemp(t, "evidence.json", `[{"name":"本地检查","status":"passed","evidence":"实际通过"}]`)

	cmd, _, err := issueCmd.Find([]string{"delivery", "deliver"})
	if err != nil {
		t.Fatal(err)
	}
	for flag, value := range map[string]string{
		"plan-file": plan, "checks-file": checks, "evidence-file": evidence,
		"delegate": "这个改动直接发吧", "repo": repo, "source-commit": head, "base-commit": head, "execution": "test-session",
	} {
		if err = cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = cmd.RunE(cmd, []string{issueID}); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, request := range *requests {
		got = append(got, request.Method+" "+strings.TrimPrefix(request.Path, "/api/issues/"+issueID))
	}
	want := []string{
		"POST /delivery-owner",
		"POST /comments",
		"PUT /delivery-plan",
		"GET /deliveries",
		"POST /comments",
		"POST /delivery-plan/confirm",
		"GET /deliveries",
		"POST /deliveries",
		"GET /deliveries",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("request order\n got: %v\nwant: %v", got, want)
	}
	for _, request := range *requests {
		if strings.HasSuffix(request.Path, "/delivery-plan/confirm") {
			if request.Body["release_after_acceptance"] != nil && request.Body["release_after_acceptance"] != true {
				t.Fatalf("unexpected release flag: %v", request.Body["release_after_acceptance"])
			}
			if !strings.Contains(request.Body["scope"].(string), "按确认方案") {
				t.Fatalf("scope not recorded: %v", request.Body["scope"])
			}
		}
	}
}

func TestDeliveryDeliverStopsOnPreflightFailure(t *testing.T) {
	repo, head := gitRepo(t)
	server, _, count := deliveryServer(t, head)
	setCLIClientEnv(t, server.URL)
	// A local env file makes the publisher refuse the candidate; the CLI must
	// stop before claiming an owner or creating any comment.
	if err := os.WriteFile(filepath.Join(repo, ".env.local"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := writeTemp(t, "plan.md", "方案\n")
	checks := writeTemp(t, "checks.json", `[{"name":"业务功能","stage":"verification"},{"name":"组合验收","stage":"integration"}]`)
	evidence := writeTemp(t, "evidence.json", `[{"name":"本地检查","status":"passed","evidence":"实际通过"}]`)

	cmd, _, err := issueCmd.Find([]string{"delivery", "deliver"})
	if err != nil {
		t.Fatal(err)
	}
	for flag, value := range map[string]string{
		"plan-file": plan, "checks-file": checks, "evidence-file": evidence,
		"delegate": "发吧", "repo": repo, "source-commit": head, "base-commit": head, "execution": "test-session",
	} {
		if err = cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = cmd.RunE(cmd, []string{"11111111-1111-4111-8111-111111111111"}); err == nil {
		t.Fatal("preflight failure must abort")
	}
	if count() != 0 {
		t.Fatalf("wrote to the API before preflight passed: %d requests", count())
	}
}

func TestDeliveryWaitStopsAtTargetPhase(t *testing.T) {
	issueID := "11111111-1111-4111-8111-111111111111"
	server, requests, _ := deliveryServer(t, strings.Repeat("a", 40))
	setCLIClientEnv(t, server.URL)

	cmd, _, err := issueCmd.Find([]string{"delivery", "wait"})
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Flags().Set("timeout", "5s"); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Flags().Set("interval", "1ms"); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Flags().Set("until", "prepared"); err != nil {
		t.Fatal(err)
	}
	if err = cmd.RunE(cmd, []string{issueID}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) < 3 || (*requests)[0].Method != http.MethodGet {
		t.Fatalf("expected repeated read-only polls until the candidate exists, got %+v", *requests)
	}
}

func TestDeliveryWaitDoesNotStopBeforeCandidate(t *testing.T) {
	issueID := "11111111-1111-4111-8111-111111111111"
	// The fake reports phase testing immediately but leaves candidate_id empty
	// for the first reads; `prepared` must ignore that.
	server, requests, _ := deliveryServer(t, strings.Repeat("a", 40))
	setCLIClientEnv(t, server.URL)
	cmd, _, err := issueCmd.Find([]string{"delivery", "wait"})
	if err != nil {
		t.Fatal(err)
	}
	for flag, value := range map[string]string{"until": "prepared", "timeout": "5s", "interval": "1ms"} {
		if err = cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = cmd.RunE(cmd, []string{issueID}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) < 3 {
		t.Fatalf("wait returned before the candidate existed: %d polls", len(*requests))
	}
}
