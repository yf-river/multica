package supervision

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResultRejectsInventedEvidenceAndPartialCoverage(t *testing.T) {
	material := Material{Evidence: Evidence{Kind: "message", ID: "m1", RunID: "r1"}, Generation: 1, Length: 100, End: 100}
	result := Result{Summary: "已核对", Efficiency: "clear", Correctness: "uncertain", Gaps: []string{"尚无真实接口结果"}, Findings: []Finding{}, Resolutions: []Resolution{}, Covered: []Evidence{{Kind: "message", ID: "m1", RunID: "r1"}}}
	if err := ValidateResult(result, []Material{material}); err != nil {
		t.Fatal(err)
	}
	result.Covered[0].ID = "m2"
	if ValidateResult(result, []Material{material}) == nil {
		t.Fatal("accepted invented coverage")
	}
	result.Covered = []Evidence{}
	if ValidateResult(result, []Material{material}) == nil {
		t.Fatal("accepted incomplete coverage")
	}
	result.Covered = []Evidence{{Kind: "message", ID: "m1", RunID: "foreign"}}
	if ValidateResult(result, []Material{material}) == nil {
		t.Fatal("accepted foreign run")
	}
}

func TestResultRequiresStructuredFieldsAndRealResolutionEvidence(t *testing.T) {
	if _, err := ParseResult(`{"summary":"所有任务完成"}`); err == nil {
		t.Fatal("accepted unsupported completion")
	}
	r := Result{Summary: "只完成材料审查", Efficiency: "clear", Correctness: "uncertain", Gaps: []string{}, Findings: []Finding{}, Resolutions: []Resolution{}, Covered: []Evidence{}}
	raw, _ := json.Marshal(r)
	if _, err := ParseResult("```json\n" + string(raw) + "\n```"); err != nil {
		t.Fatal(err)
	}
	r.Resolutions = []Resolution{{ID: "f1", Reason: "没有修复证据"}}
	if ValidateResult(r, nil) == nil {
		t.Fatal("accepted evidence-free resolution")
	}
}

func TestPromptPreservesSupervisorNotesAndIncompleteSlice(t *testing.T) {
	prompt := BuildPrompt("b1", `{"title":"修复接口"}`, "此前只验证本地", "[]", []Material{{Evidence: Evidence{Kind: "comment", ID: "c1"}, Text: "/note 用户体验不完整，请核验", Offset: 0, End: 10, Length: 100, Note: true}})
	for _, want := range []string{"/note 用户体验不完整", "不能直接当作开发指令", "不得声称全量完成", "即使end<length，也必须列出本轮已读片段的引用", "\"supervisor_note\":true", "此前只验证本地", `"covered":[{"kind":"comment","id":"c1"}]`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
	if _, err := sourceSQL("message; DROP TABLE issue"); err == nil {
		t.Fatal("accepted arbitrary query kind")
	}
}

func TestInvestigationCitationDoesNotReplaceInitialCoverage(t *testing.T) {
	m := Material{Evidence: Evidence{Kind: "message", ID: "m1", RunID: "run-A"}}
	capture := Material{Evidence: Evidence{Kind: "investigation", ID: "capture", RunID: "run-A"}}
	r := Result{Summary: "核对代码", Efficiency: "clear", Correctness: "problem", Gaps: []string{}, Covered: []Evidence{m.Evidence}, Resolutions: []Resolution{}, Findings: []Finding{{Code: "mapping", Title: "字段错误", Description: "实际取用户ID", Impact: "租户错误", Recommendation: "核对租户字段", Confidence: "confirmed", Evidence: []Evidence{capture.Evidence}}}}
	if err := ValidateResult(r, []Material{m}, capture); err != nil {
		t.Fatal(err)
	}
	if ValidateResult(r, []Material{m}) == nil {
		t.Fatal("accepted capture from another batch")
	}
	r.Covered = []Evidence{capture.Evidence}
	if ValidateResult(r, []Material{m}, capture) == nil {
		t.Fatal("investigation replaced unread initial material")
	}
	r.Covered = []Evidence{m.Evidence}
	r.Findings[0].Evidence[0].RunID = "foreign"
	if ValidateResult(r, []Material{m}, capture) == nil {
		t.Fatal("accepted capture from another source run")
	}
}

func TestAdjacentContextIsEvidenceButNeverCoverage(t *testing.T) {
	primary := Material{Evidence: Evidence{Kind: "message", ID: "new", RunID: "run"}}
	previous := Material{Evidence: Evidence{Kind: "message", ID: "old", RunID: "run"}, ContextOnly: true, Text: "tool failed"}
	r := Result{Covered: []Evidence{primary.Evidence}, Findings: []Finding{{Code: "ignored_error", Title: "错误未处理", Description: "工具失败后继续", Impact: "结果不可信", Recommendation: "核对工具失败", Confidence: "confirmed", Evidence: []Evidence{primary.Evidence, previous.Evidence}}}}
	if err := ValidateResult(r, []Material{primary, previous}); err != nil {
		t.Fatal(err)
	}
	r.Covered = append(r.Covered, previous.Evidence)
	if ValidateResult(r, []Material{primary, previous}) == nil {
		t.Fatal("context incorrectly counted as new coverage")
	}
	prompt := BuildPrompt("batch", "{}", "", "[]", []Material{primary, previous})
	if !strings.Contains(prompt, `"covered":[{"kind":"message","id":"new","run_id":"run"}]`) || !strings.Contains(prompt, "tool failed") {
		t.Fatal("context or primary coverage missing")
	}
}
