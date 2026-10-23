package main

import (
	"github.com/spf13/cobra"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDeliveryDiagnosticCLIQuery(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Int("limit", 2, "")
	cmd.Flags().String("since", "2026-10-24T01:00:00+08:00", "")
	cmd.Flags().String("after", "event-id", "")
	cmd.Flags().String("delivery", "delivery-id", "")
	q, err := deliveryDiagnosticQuery(cmd)
	if err != nil || q.Get("limit") != "2" || q.Get("delivery_id") != "delivery-id" {
		t.Fatal(q, err)
	}
	if q.Encode() == "" {
		t.Fatal("missing escaped cursor")
	}
	if err = cmd.Flags().Set("since", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = deliveryDiagnosticQuery(cmd); err == nil {
		t.Fatal("after without since accepted")
	}
	if err = cmd.Flags().Set("limit", "201"); err != nil {
		t.Fatal(err)
	}
	if _, err = deliveryDiagnosticQuery(cmd); err == nil {
		t.Fatal("unbounded limit")
	}
}

// 真实 CLI 路由配受控 HTTP 服务，只允许一个 GET；任何写请求都会失败。
func TestDeliveryDiagnosticCLIReadOnly(t *testing.T) {
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Method != "GET" || r.URL.Path != "/api/issues/11111111-1111-4111-8111-111111111111/deliveries/diagnostics" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"summary":{"phase":"testing"}}`))
	}))
	defer srv.Close()
	for _, key := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_DAEMON_PORT"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace")
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd, _, err := issueCmd.Find([]string{"delivery", "diagnose"})
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.RunE(cmd, []string{"11111111-1111-4111-8111-111111111111"}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("request count %d", count)
	}
}
