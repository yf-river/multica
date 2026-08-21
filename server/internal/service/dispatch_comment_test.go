package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDispatchCommentFromTaskMessages(t *testing.T) {
	agentID := "11111111-1111-1111-1111-111111111111"
	otherAgentID := "22222222-2222-2222-2222-222222222222"
	dispatch := "调度 01-需求澄清：请继续。 [@01-需求澄清](mention://agent/" + agentID + ")"

	tests := []struct {
		name     string
		messages []db.TaskMessage
		want     string
	}{
		{
			name: "uses latest single dispatch mention",
			messages: []db.TaskMessage{
				textTaskMessage("中间总结，无 mention"),
				textTaskMessage(dispatch),
				textTaskMessage("已完成 PM 首轮调度，等待下一阶段。"),
			},
			want: dispatch,
		},
		{
			name: "ignores multiple mentions",
			messages: []db.TaskMessage{
				textTaskMessage("调度多个阶段 [@01](mention://agent/" + agentID + ") [@02](mention://agent/" + otherAgentID + ")"),
			},
			want: "",
		},
		{
			name: "ignores non dispatch mention",
			messages: []db.TaskMessage{
				textTaskMessage("仅供参考 [@01](mention://agent/" + agentID + ")"),
			},
			want: "",
		},
		{
			name: "ignores non text messages",
			messages: []db.TaskMessage{
				{Type: "tool_result", Content: pgtype.Text{String: dispatch, Valid: true}},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dispatchCommentFromTaskMessages(tt.messages); got != tt.want {
				t.Fatalf("dispatchCommentFromTaskMessages() = %q, want %q", got, tt.want)
			}
		})
	}
}

func textTaskMessage(content string) db.TaskMessage {
	return db.TaskMessage{
		Type:    "text",
		Content: pgtype.Text{String: content, Valid: true},
	}
}
