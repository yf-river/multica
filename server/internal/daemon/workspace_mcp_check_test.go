package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

func TestWorkspaceMcpCheckHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprint(sse), func(t *testing.T) {
			calls, deletes := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("credential missing")
				}
				if r.Method == "DELETE" {
					deletes++
					w.WriteHeader(204)
					return
				}
				var p struct {
					ID     int            `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&p)
				var result any
				switch p.Method {
				case "initialize":
					w.Header().Set("Mcp-Session-Id", "test-session")
					result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}}
				case "notifications/initialized":
					w.WriteHeader(202)
					return
				case "tools/list":
					calls++
					if r.Header.Get("Mcp-Session-Id") != "test-session" {
						t.Error("session missing")
					}
					if r.Header.Get("MCP-Protocol-Version") != "2025-03-26" {
						t.Error("negotiated version missing")
					}
					if calls == 1 {
						result = map[string]any{"tools": []map[string]string{{"name": "read"}}, "nextCursor": "page2"}
					} else {
						if p.Params["cursor"] != "page2" {
							t.Error("cursor missing")
						}
						result = map[string]any{"tools": []map[string]string{{"name": "read"}, {"name": "search"}}}
					}
				default:
					t.Errorf("unexpected method %s", p.Method)
				}
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p.ID, "result": result})
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", b)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write(b)
				}
			}))
			defer srv.Close()
			cfg, _ := json.Marshal(map[string]any{"url": srv.URL, "headers": map[string]string{"Authorization": "Bearer secret"}})
			count, code := checkWorkspaceMcp(context.Background(), cfg)
			if code != "" || count != 2 || calls != 2 || deletes != 1 {
				t.Fatalf("count=%d code=%s calls=%d deletes=%d", count, code, calls, deletes)
			}
		})
	}
}
func TestWorkspaceMcpCheckFailures(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, "private-upstream-secret")
		}))
		cfg, _ := json.Marshal(map[string]string{"url": srv.URL})
		_, code := checkWorkspaceMcp(context.Background(), cfg)
		srv.Close()
		want := "connection_failed"
		if status < 500 {
			want = "authentication_failed"
		}
		if code != want {
			t.Fatalf("%d: %s", status, code)
		}
	}
}
func TestWorkspaceMcpCheckStdioAndCancellation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	script := `import sys,json
for line in sys.stdin:
 p=json.loads(line)
 if 'id' not in p: continue
 result={'protocolVersion':'2025-03-26','capabilities':{'tools':{}}} if p['method']=='initialize' else {'tools':[{'name':'read'}]}
 print(json.dumps({'jsonrpc':'2.0','id':p['id'],'result':result}),flush=True)
`
	cfg, _ := json.Marshal(map[string]any{"command": python, "args": []string{"-c", script}})
	count, code := checkWorkspaceMcp(context.Background(), cfg)
	if count != 1 || code != "" {
		t.Fatalf("%d %s", count, code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cfg, _ = json.Marshal(map[string]any{"command": python, "args": []string{"-c", "import time;time.sleep(120)"}})
	start := time.Now()
	_, code = checkWorkspaceMcp(ctx, cfg)
	if code != "timeout" || time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation: %s %s", code, time.Since(start))
	}
}
