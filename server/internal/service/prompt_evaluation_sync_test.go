package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPromptEvaluationAgentVerdictsFromMarkdownJSON(t *testing.T) {
	output := strings.Join([]string{
		"Agent 输出：完成训练评估并给出验收证据。",
		"```json",
		`{"用例结果":[{"case_index":0,"status":"通过","output":"完成","failure_reason":"无","evidence":{"命中":["验收条件"],"缺失":[]}}],"评估结论":"Agent 已返回结构化逐用例评估，全部用例通过"}`,
		"```",
	}, "\n")
	result, err := json.Marshal(map[string]any{"status": "completed", "output": output})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	verdicts, ok := promptEvaluationAgentVerdictsFromTask(
		db.PromptEvaluationRun{TotalCases: 1},
		db.AgentTaskQueue{Result: result},
		[]db.TaskMessage{{Seq: 1, Type: "text", Content: pgtype.Text{String: output, Valid: true}}},
	)
	if !ok {
		t.Fatalf("expected structured verdicts from markdown json")
	}
	if len(verdicts) != 1 || verdicts[0].Status != "通过" || verdicts[0].FailureReason != "无" {
		t.Fatalf("verdicts = %+v", verdicts)
	}
}
