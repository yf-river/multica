package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// Regression tests for the PR #9423 review (MUL-8154), derived from the
// reviewer's reproduction probes.

// registrationPrecheckBarrier delays every registration's owner pre-check
// until a second one has also read (or the window closes). Before the
// registration lock both reads saw an empty daemon and both inserted; with
// the lock the second read cannot start until the first registration is done,
// so the barrier just times out and lets each proceed in turn.
type registrationPrecheckBarrier struct {
	arrivals atomic.Int32
	release  chan struct{}
	once     sync.Once
	window   time.Duration
}

// barrierTxStarter wraps the handler's TxStarter so every transaction it
// opens routes Query through the barrier.
type barrierTxStarter struct {
	inner   txStarter
	barrier *registrationPrecheckBarrier
}

func (s barrierTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return barrierTx{Tx: tx, barrier: s.barrier}, nil
}

type barrierTx struct {
	pgx.Tx
	barrier *registrationPrecheckBarrier
}

func (t barrierTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := t.Tx.Query(ctx, query, args...)
	if err != nil {
		return rows, err
	}
	t.barrier.wait(query)
	return rows, nil
}

func (d *registrationPrecheckBarrier) wait(query string) {
	if !strings.Contains(query, "name: ListRuntimeOwnersByDaemon") {
		return
	}
	if d.arrivals.Add(1) == 2 {
		d.once.Do(func() { close(d.release) })
	}
	select {
	case <-d.release:
	case <-time.After(d.window):
	}
}

func TestDaemonRegister_ConcurrentDifferentProvidersAndProfilesHaveOneOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	profileID := insertRuntimeProfileFixture(t, ctx, "Race profile "+uuid.NewString()[:8], "pi", "race-wrapper")

	cases := []struct {
		name     string
		runtimes [2]map[string]any
	}{
		{"providers", [2]map[string]any{
			{"name": "claude", "type": "claude", "status": "online"},
			{"name": "codex", "type": "codex", "status": "online"},
		}},
		{"provider and profile", [2]map[string]any{
			{"name": "claude", "type": "claude", "status": "online"},
			{"name": "race profile", "type": "pi", "profile_id": profileID, "status": "online"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			second := dbfx.User(t, "race second owner", "race-"+uuid.NewString()+"@example.com")
			dbfx.Member(t, testWorkspaceID, second, "member")
			daemonID := "race-" + uuid.NewString()
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID)
			})

			// The pre-check runs on the registration transaction, so the
			// barrier is injected at the TxStarter level, not h.Queries.
			h := *testHandler
			h.TxStarter = barrierTxStarter{
				inner:   testHandler.TxStarter,
				barrier: &registrationPrecheckBarrier{release: make(chan struct{}), window: 500 * time.Millisecond},
			}
			users := []string{testUserID, second}
			codes := make([]int, 2)
			var wg sync.WaitGroup
			for i := range users {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					w := httptest.NewRecorder()
					h.DaemonRegister(w, newRequestAs(users[i], "POST", "/api/daemon/register", map[string]any{
						"workspace_id": testWorkspaceID, "daemon_id": daemonID,
						"runtimes": []map[string]any{tc.runtimes[i]},
					}))
					codes[i] = w.Code
				}(i)
			}
			wg.Wait()

			var owners, rows int
			dbfx.QueryRow(t, `SELECT count(DISTINCT owner_id), count(*) FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`,
				testWorkspaceID, daemonID).Scan(&owners, &rows)
			if owners != 1 || rows != 1 {
				t.Fatalf("daemon has %d owners across %d rows after concurrent registration (statuses %v), want exactly one", owners, rows, codes)
			}
			ok, forbidden := 0, 0
			for _, c := range codes {
				switch c {
				case http.StatusOK:
					ok++
				case http.StatusForbidden:
					forbidden++
				}
			}
			if ok != 1 || forbidden != 1 {
				t.Fatalf("statuses = %v, want one 200 and one 403", codes)
			}
		})
	}
}

// TestDaemonRPC_QoderClaimRevalidatesConnectionCredential: a WS RPC replays
// the claim without an Authorization header, so the Qoder revocation check
// must use the connection's credential — accepting it while valid and
// refusing it once revoked.
func TestDaemonToken_OldOwnersCredentialCannotReachReplacementOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	daemonID := "reused-" + uuid.NewString()
	oldID := dbfx.Runtime(t, "reuse old runtime", testutil.Cols{"daemon_id": daemonID, "provider": "claude"})
	old, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(oldID))
	if err != nil {
		t.Fatal(err)
	}
	raw, params, err := remoteMCPDaemonTokenForClaim(AgentTaskResponse{
		RemoteMCPConnections: []remotemcp.Connection{{ContributionKey: "reuse"}},
	}, old)
	if err != nil || len(params) != 1 {
		t.Fatalf("mint: %v", err)
	}
	if uuidToString(params[0].OwnerID) != testUserID {
		t.Fatalf("Remote MCP token owner = %q, want the runtime owner", uuidToString(params[0].OwnerID))
	}
	token, err := testHandler.Queries.CreateDaemonToken(ctx, params[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM daemon_token WHERE id = $1`, token.ID)
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID)
	})
	authed := func(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
		req.Header.Del("X-User-ID")
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(h).ServeHTTP(w, req)
		return w
	}
	heartbeat := func(runtimeID string) int {
		return authed(testHandler.DaemonHeartbeat, newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID})).Code
	}
	if code := heartbeat(oldID); code != http.StatusOK {
		t.Fatalf("positive control: owner's token heartbeat = %d", code)
	}
	// A WS connection the old owner opened before the hand-over.
	var openIdentity daemonws.ClientIdentity
	authed(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		if openIdentity, ok = testHandler.buildDaemonWebSocketIdentity(w, r, []string{oldID}, ""); !ok {
			t.Fatalf("positive control: owner's WS handshake refused: %d", w.(*httptest.ResponseRecorder).Code)
		}
	}, httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil))

	// A deletes the last runtime; B registers the same daemon_id.
	testutil.Call(t, testHandler.DeleteAgentRuntime,
		withURLParams(newRequestAs(testUserID, "DELETE", "/api/runtimes/"+oldID, nil), "runtimeId", oldID)).Want(http.StatusOK)
	second := dbfx.User(t, "replacement owner", "replacement-"+uuid.NewString()+"@example.com")
	dbfx.Member(t, testWorkspaceID, second, "member")
	testutil.Call(t, testHandler.DaemonRegister, registerAs(second, daemonID)).Want(http.StatusOK)
	var newID string
	dbfx.QueryRow(t, `SELECT id FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2 AND owner_id = $3`,
		testWorkspaceID, daemonID, second).Scan(&newID)
	agentID := testutil.New(testPool, testWorkspaceID, second).Agent(t, "replacement agent", newID)
	issueID := dbfx.Issue(t, "replacement task")
	taskID := seedQueuedIssueTask(t, ctx, agentID, newID, issueID)

	if code := heartbeat(newID); code != http.StatusNotFound {
		t.Fatalf("old token heartbeat on the replacement runtime = %d, want 404", code)
	}
	w := authed(testHandler.ClaimTaskByRuntime,
		withURLParams(newRequest("POST", "/api/daemon/runtimes/"+newID+"/tasks/claim", nil), "runtimeId", newID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("old token claim on the replacement runtime = %d %s, want 404", w.Code, w.Body.String())
	}
	authed(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := testHandler.buildDaemonWebSocketIdentity(w, r, []string{newID}, ""); ok {
			t.Fatal("old token opened a WS onto the replacement runtime")
		}
	}, httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil))
	body, _ := json.Marshal(map[string]any{"daemon_id": daemonID, "runtime_ids": []string{newID}, "max_tasks": 1})
	status, respBody, err := testHandler.DaemonRPCHandler(ctx, openIdentity, "tasks.claim", body)
	if err != nil || status != http.StatusOK || strings.Contains(string(respBody), taskID) {
		t.Fatalf("already-open WS claimed on the replacement runtime: status=%d err=%v body=%s", status, err, respBody)
	}
	if got := taskStatusOf(t, taskID); got != "queued" {
		t.Fatalf("replacement owner's task status = %q, want queued", got)
	}
	var tokens int
	dbfx.QueryRow(t, `SELECT count(*) FROM task_token WHERE task_id = $1`, taskID).Scan(&tokens)
	if tokens != 0 {
		t.Fatalf("%d task tokens minted for the replacement owner's task", tokens)
	}
}

func TestDaemonToken_ReplacedOwnerAndUnboundTokensCannotRegisterOrAct(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		unbound bool
	}{{"bound to previous owner", false}, {"unbound legacy token", true}} {
		t.Run(tc.name, func(t *testing.T) {
			daemonID := "reuse-reg-" + uuid.NewString()
			oldID := dbfx.Runtime(t, "reuse-reg old", testutil.Cols{"daemon_id": daemonID, "provider": "claude"})
			old, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(oldID))
			if err != nil {
				t.Fatal(err)
			}
			raw, params, err := remoteMCPDaemonTokenForClaim(AgentTaskResponse{
				RemoteMCPConnections: []remotemcp.Connection{{ContributionKey: "reuse-reg"}},
			}, old)
			if err != nil {
				t.Fatal(err)
			}
			if tc.unbound {
				params[0].OwnerID = pgtype.UUID{}
			}
			token, err := testHandler.Queries.CreateDaemonToken(ctx, params[0])
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM daemon_token WHERE id = $1`, token.ID)
				testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID)
			})
			call := func(h http.HandlerFunc, req *http.Request) int {
				req.Header.Del("X-User-ID")
				req.Header.Set("Authorization", "Bearer "+raw)
				w := httptest.NewRecorder()
				middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(h).ServeHTTP(w, req)
				return w.Code
			}
			register := func() int {
				return call(testHandler.DaemonRegister, newRequest("POST", "/api/daemon/register", map[string]any{
					"workspace_id": testWorkspaceID, "daemon_id": daemonID,
					"runtimes": []map[string]any{{"type": "claude", "name": "written by old token", "status": "offline"}},
				}))
			}
			if tc.unbound {
				// Refused even while the issuing owner still owns the daemon.
				if code := call(testHandler.DaemonHeartbeat, newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": oldID})); code != http.StatusNotFound {
					t.Fatalf("unbound token heartbeat = %d, want 404", code)
				}
				if code := register(); code != http.StatusForbidden {
					t.Fatalf("unbound token register = %d, want 403", code)
				}
			} else if code := register(); code != http.StatusOK {
				t.Fatalf("positive control: bound token re-registers its owner's daemon = %d", code)
			}

			testutil.Call(t, testHandler.DeleteAgentRuntime,
				withURLParams(newRequestAs(testUserID, "DELETE", "/api/runtimes/"+oldID, nil), "runtimeId", oldID)).Want(http.StatusOK)
			second := dbfx.User(t, "reuse-reg replacement", "reuse-reg-"+uuid.NewString()+"@example.com")
			dbfx.Member(t, testWorkspaceID, second, "member")
			testutil.Call(t, testHandler.DaemonRegister, registerAs(second, daemonID)).Want(http.StatusOK)
			var newID string
			dbfx.QueryRow(t, `SELECT id FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID).Scan(&newID)

			if code := register(); code != http.StatusForbidden {
				t.Fatalf("old token register over the replacement owner = %d, want 403", code)
			}
			rt, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(newID))
			if err != nil {
				t.Fatal(err)
			}
			if rt.Status != "online" || rt.Name == "written by old token" || uuidToString(rt.OwnerID) != second {
				t.Fatalf("replacement runtime mutated: status=%s name=%q owner=%s", rt.Status, rt.Name, uuidToString(rt.OwnerID))
			}
			if code := call(testHandler.DaemonHeartbeat, newRequest("POST", "/api/daemon/heartbeat", map[string]any{"runtime_id": newID})); code != http.StatusNotFound {
				t.Fatalf("old token heartbeat on the replacement runtime = %d, want 404", code)
			}
		})
	}
}

// Fourth #9423 review: a qoder-managed-* daemon token gets the owner-binding
// exemption only with a live cloud_agent_connection, whatever runtime type
// the body declares. Pre-upgrade tokens under that prefix were issued for
// ordinary runtimes and have no connection.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
