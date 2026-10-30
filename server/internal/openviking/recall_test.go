package openviking

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRecallReservesMemoryBudgetWhenSharedResourcesAreLarge(t *testing.T) {
	var targets []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-OpenViking-User") != "agent-personal" {
			t.Error("wrong user")
		}
		var input struct {
			URI string `json:"target_uri"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		targets = append(targets, input.URI)
		var result any
		switch input.URI {
		case "viking://user/agent-personal/memories":
			result = map[string]any{"memories": []map[string]string{{"uri": input.URI + "/preferences/report.md", "content": "用户验收项目代号 HarborMap，偏好中文且结论在前。"}}}
		case "viking://resources":
			result = map[string]any{"resources": []map[string]string{{"uri": "viking://resources/large.md", "content": strings.Repeat("很长的共享资料", 10000)}, {"uri": "viking://resources/second.md", "content": "第二条资料也应可见"}}}
		default:
			t.Errorf("unexpected scope %s", input.URI)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": result})
	}))
	defer upstream.Close()
	client := &Client{URL: upstream.URL, HTTP: upstream.Client()}
	text, err := client.Recall(context.Background(), "agent-personal", "之前项目叫什么？")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || !strings.Contains(text, "HarborMap") || !strings.Contains(text, "viking://resources/large.md") || !strings.Contains(text, "第二条资料也应可见") || len(text) > 24000 || !utf8.ValidString(text) {
		t.Fatalf("recall lost a scope or exceeded budget: targets=%v bytes=%d", targets, len(text))
	}
}
