package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const strictPromptEvaluationAgentVerdictJSON = `{"schema_version":1,"schema":"multica.training_evaluation.agent_verdict.v1","case_results":[{"case_index":0,"status":"通过","output":{"摘要":"完成"},"failure_reason":"无","conclusion":"中文结论","evidence":{"命中":["验收条件"],"缺失":[]}}],"summary":{"total_cases":1,"passed_cases":1,"failed_cases":0,"failure_reason":"无","conclusion":"全部通过"}}`

func TestPromptEvaluationAgentVerdictsFromMarkdownJSON(t *testing.T) {
	output := strings.Join([]string{
		"Agent 输出：完成训练评估并给出验收证据。",
		"```json",
		strictPromptEvaluationAgentVerdictJSON,
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

func TestPromptEvaluationAgentVerdictsFromStrictSchema(t *testing.T) {
	output := strings.Join([]string{
		"Agent 输出：完成训练评估并给出规范 verdict。",
		"```json",
		strictPromptEvaluationAgentVerdictJSON,
		"```",
	}, "\n")
	result, err := json.Marshal(map[string]any{"status": "completed", "output": output})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	verdicts, ok := promptEvaluationAgentVerdictsFromTask(
		db.PromptEvaluationRun{TotalCases: 1},
		db.AgentTaskQueue{Result: result},
		nil,
	)
	if !ok {
		t.Fatalf("expected structured verdicts from strict schema")
	}
	if len(verdicts) != 1 || verdicts[0].Status != "通过" || verdicts[0].Evidence["解析契约"] != "multica.training_evaluation.agent_verdict.v1" {
		t.Fatalf("verdicts = %+v", verdicts)
	}
}

func TestPromptEvaluationFailureReasonFromMessagesRecognizesQuota(t *testing.T) {
	reason := promptEvaluationFailureReasonFromMessages([]db.TaskMessage{{
		Seq:     1,
		Type:    "text",
		Content: pgtype.Text{String: "429 当前无可用Token额度，如需申请，请联系您所在团队的负责人或HRBP，也可以继续使用混元模型开展工作。额度查看：aitoken.woa.com  (trace/session)", Valid: true},
	}})
	if !strings.Contains(reason, "模型额度不足") || strings.Contains(reason, "trace/session") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestPromptEvaluationFailureReasonFromMessagesTruncatesLongText(t *testing.T) {
	reason := promptEvaluationFailureReasonFromMessages([]db.TaskMessage{{
		Seq:     1,
		Type:    "text",
		Content: pgtype.Text{String: strings.Repeat("失败", 120), Valid: true},
	}})
	if len([]rune(reason)) > 183 || !strings.HasSuffix(reason, "...") {
		t.Fatalf("reason = %q", reason)
	}
}
