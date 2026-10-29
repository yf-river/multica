package handler

import (
	"context"
	"strings"
	"testing"
)

func TestOpenVikingTransactionalSourceCapture(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	agent := createHandlerTestAgent(t, "OV-source-test", []byte("[]"))
	session := createHandlerTestChatSession(t, agent)
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET multica.openviking_enabled='true'"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "RESET multica.openviking_enabled")
	defer conn.Exec(ctx, "DELETE FROM openviking_event WHERE chat_session_id=$1", session)
	secret := "api_key=preserved-original-" + strings.Repeat("完整", 150000)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat_message(chat_session_id,role,content) VALUES($1,'user',$2)`, session, secret); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM openviking_event WHERE chat_session_id=$1", session).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rolled back source archived: %d %v", n, err)
	}
	var id string
	if err = conn.QueryRow(ctx, `INSERT INTO chat_message(chat_session_id,role,content) VALUES($1,'user',$2) RETURNING id::text`, session, secret).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE chat_message SET content='edited' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM chat_message WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `SELECT payload->>'operation',payload->'row'->>'content',target_user FROM openviking_event WHERE chat_session_id=$1 ORDER BY created_at,id`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	operations := []string{"INSERT", "UPDATE", "DELETE"}
	contents := []string{secret, "edited", "edited"}
	i := 0
	for rows.Next() {
		var op, content, user string
		if err = rows.Scan(&op, &content, &user); err != nil {
			t.Fatal(err)
		}
		if i >= 3 || op != operations[i] || content != contents[i] || user != "agent-"+agent {
			t.Fatalf("incorrect original source %d op=%s user=%s bytes=%d", i, op, user, len(content))
		}
		i++
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != 3 {
		t.Fatalf("wanted all three durable revisions, got %d", i)
	}
}

func TestOpenVikingSessionMetadataCapture(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	agent := createHandlerTestAgent(t, "OV-session-test", []byte("[]"))
	session := createHandlerTestChatSession(t, agent)
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET multica.openviking_enabled='true'"); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "RESET multica.openviking_enabled")
	defer conn.Exec(ctx, "DELETE FROM openviking_event WHERE chat_session_id=$1", session)
	if _, err = conn.Exec(ctx, "UPDATE chat_session SET title='完整会话元数据' WHERE id=$1", session); err != nil {
		t.Fatal(err)
	}
	var title, user, operation string
	if err = conn.QueryRow(ctx, `SELECT payload->'row'->>'title',target_user,payload->>'operation' FROM openviking_event WHERE chat_session_id=$1 AND kind='chat_session'`, session).Scan(&title, &user, &operation); err != nil {
		t.Fatal(err)
	}
	if title != "完整会话元数据" || user != "agent-"+agent || operation != "UPDATE" {
		t.Fatalf("incorrect session metadata: %s %s %s", title, user, operation)
	}
}
