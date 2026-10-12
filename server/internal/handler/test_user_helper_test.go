package handler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

var testUserSeq atomic.Int64

func createTestUserAndMember(t *testing.T, role string) string {
	t.Helper()
	seq := testUserSeq.Add(1)
	var userID string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, fmt.Sprintf("test-user-%d", seq), fmt.Sprintf("test-user-%d@multica.ai", seq)).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if role != "" {
		if _, err := testPool.Exec(context.Background(), `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, parseUUID(testWorkspaceID), parseUUID(userID), role); err != nil {
			t.Fatalf("create test member: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, parseUUID(userID))
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, parseUUID(userID))
	})
	return userID
}
