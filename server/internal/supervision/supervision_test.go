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
	prompt, err := BuildPrompt("b1", `{"title":"修复接口"}`, "此前只验证本地", "[]", []Material{{Evidence: Evidence{Kind: "comment", ID: "c1"}, Text: "/note 用户体验不完整，请核验", Offset: 0, End: 10, Length: 100, Note: true}})
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Current         map[string]string `json:"current"`
		PreviousSummary string            `json:"previous_summary"`
		Materials       []Material        `json:"materials"`
		Covered         []Evidence        `json:"covered"`
	}
	if err := json.Unmarshal([]byte(prompt), &input); err != nil {
		t.Fatal(err)
	}
	if input.Current["title"] != "修复接口" || input.PreviousSummary != "此前只验证本地" || len(input.Materials) != 1 || !input.Materials[0].Note || input.Materials[0].End != 10 || input.Materials[0].Length != 100 || len(input.Covered) != 1 || input.Covered[0].Kind != "comment" {
		t.Fatalf("lost batch input: %+v", input)
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
	prompt, err := BuildPrompt("batch", "{}", "", "[]", []Material{primary, previous})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"covered":[{"kind":"message","id":"new","run_id":"run"}]`) || !strings.Contains(prompt, "tool failed") {
		t.Fatal("context or primary coverage missing")
	}
}

func TestBatchInputRejectsInvalidJSON(t *testing.T) {
	if _, err := BuildPrompt("batch", "invalid-json", "", "[]", nil); err == nil {
		t.Fatal("invalid structured input accepted")
	}
}

func TestFindingValidationNamesMissingField(t *testing.T) {
	m := Material{Evidence: Evidence{Kind: "message", ID: "source"}}
	r := Result{Covered: []Evidence{m.Evidence}, Findings: []Finding{{Title: "确定的映射错误", Description: "输入被遗漏", Impact: "接口接收错误数据", Recommendation: "保留用户输入", Confidence: "confirmed", Evidence: []Evidence{m.Evidence}}}}
	if err := ValidateResult(r, []Material{m}); err == nil || !strings.Contains(err.Error(), "findings[0].code") {
		t.Fatalf("missing field not actionable: %v", err)
	}
	r.Findings[0].Code = "mapping_mismatch"
	r.Findings[0].Title = ""
	if err := ValidateResult(r, []Material{m}); err == nil || !strings.Contains(err.Error(), "findings[0].title") {
		t.Fatalf("wrong next error: %v", err)
	}
	r.Findings[0].Title = "确定的映射错误"
	if err := ValidateResult(r, []Material{m}); err != nil {
		t.Fatal(err)
	}
}

func TestReadReceiptsRequireRealSuccessfulMaterialPages(t *testing.T) {
	receipts := map[string]Receipt{"tail": {Tool: "supervision_material", Path: "message/source", Generation: 2, Offset: 8000, End: 15000, Length: 15000}, "index": {Tool: "supervision_material", Path: "index"}, "failed": {Tool: "supervision_material", Path: "message/source", Generation: 2, Offset: 8000, End: 15000, Length: 15000, Error: "已删除"}}
	pages, err := receiptMaterials(Result{ReadReceipts: []string{"tail"}}, receipts)
	if err != nil || len(pages) != 1 || pages[0].Generation != 2 {
		t.Fatalf("valid page rejected: %v", err)
	}
	for _, ids := range [][]string{{"unknown"}, {"index"}, {"failed"}, {"tail", "tail"}} {
		if _, err := receiptMaterials(Result{ReadReceipts: ids}, receipts); err == nil {
			t.Fatalf("accepted invalid receipts: %v", ids)
		}
	}
}

func TestContiguousReadDoesNotJumpOverUnreadText(t *testing.T) {
	pages := []Material{{Offset: 16000, End: 23000}, {Offset: 0, End: 8000}}
	if got := ContiguousEnd(0, pages); got != 8000 {
		t.Fatalf("jumped unread gap: %d", got)
	}
	pages = append(pages, Material{Offset: 7000, End: 17000})
	if got := ContiguousEnd(0, pages); got != 23000 {
		t.Fatalf("overlap not merged: %d", got)
	}
	if got := ContiguousEnd(24000, pages); got != 24000 {
		t.Fatal("cursor moved backwards")
	}
}
