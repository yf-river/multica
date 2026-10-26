package handler

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestNativeTaskCredentialGenerationFence(t *testing.T) {
	for _, provider := range []string{"codex", "codebuddy", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := newSupplementFixture(t, provider, "dispatched", false)
			generation := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
			dbfx.Exec(t, `UPDATE agent_task_queue SET dispatched_at=$2 WHERE id=$1`, f.taskID, generation)
			dbfx.Cleanup(t, `DELETE FROM task_token WHERE task_id=$1`, f.taskID)
			mint := func() string {
				t.Helper()
				token := "mat_" + uuid.NewString()
				_, err := testHandler.Queries.CreateTaskToken(t.Context(), db.CreateTaskTokenParams{
					TokenHash: auth.HashToken(token), TaskID: parseUUID(f.taskID), AgentID: parseUUID(f.agentID),
					WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID),
					ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
				})
				if err != nil {
					t.Fatal(err)
				}
				return token
			}
			check := func(token string, status int) {
				t.Helper()
				called := false
				h := middleware.Auth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					if r.Header.Get("X-Task-ID") != f.taskID || r.Header.Get("X-Agent-ID") != f.agentID {
						t.Error("authenticated native task identity changed")
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				r := newRequest(http.MethodGet, "/api/native-credential-test", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				testutil.Call(t, h.ServeHTTP, r).Want(status)
				if called != (status == http.StatusNoContent) {
					t.Fatal("credential fence did not control handler access")
				}
			}
			old := mint()
			dbfx.Exec(t, `UPDATE task_token SET created_at=$2 WHERE token_hash=$1`, auth.HashToken(old), generation.Add(time.Second))
			check(old, http.StatusNoContent)
			dbfx.Exec(t, `UPDATE agent_task_queue SET status='running' WHERE id=$1`, f.taskID)
			check(old, http.StatusNoContent)

			// 终态事务回滚后，当前轮次的凭据仍可使用。
			tx, err := testPool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if _, err := tx.Exec(t.Context(), `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, f.taskID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.New(tx).GetTaskTokenByHash(t.Context(), auth.HashToken(old)); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("terminal transaction admitted old credential: %v", err)
			}
			if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			check(old, http.StatusNoContent)

			for _, state := range []string{"queued", "completed", "failed", "cancelled"} {
				dbfx.Exec(t, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, f.taskID, state)
				check(old, http.StatusUnauthorized)
			}
			// 重新认领同一个任务后，旧行即使未删除也不能恢复权限。
			dbfx.Exec(t, `UPDATE agent_task_queue SET status='dispatched', dispatched_at=$2 WHERE id=$1`, f.taskID, generation.Add(2*time.Second))
			check(old, http.StatusUnauthorized)
			fresh := mint()
			check(fresh, http.StatusNoContent)
			dbfx.Exec(t, `UPDATE agent_runtime SET provider='claude' WHERE id=$1`, f.runtimeID)
			check(old, http.StatusUnauthorized)
			check(fresh, http.StatusNoContent)
			if n := dbfx.Count(t, `SELECT count(*) FROM task_token WHERE task_id=$1`, f.taskID); n != 2 {
				t.Fatal("test did not retain both credential generations")
			}
			dbfx.Exec(t, `UPDATE task_token SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, auth.HashToken(fresh))
			check(fresh, http.StatusUnauthorized)
		})
	}
}
