package handler

import (
	"strings"
	"testing"
)

// TestReviewHandoffRequiresSubmitForEveryVerdict 固定本轮说明和审查员指令一致：三种结论都要
// 走 review submit。旧说明把「通过不用改状态」和「打回或停滞必须提交」并列，审查员据此只发
// 评论不交结论，轮次停在派发态、通过标记也不会落下。
func TestReviewHandoffRequiresSubmitForEveryVerdict(t *testing.T) {
	handoff := buildReviewHandoff(1, strings.Repeat("a", 40), "", "")
	for _, want := range []string{"review submit", "通过", "打回", "停滞"} {
		if !strings.Contains(handoff, want) {
			t.Fatalf("本轮审查说明缺少 %q，审查员会漏交结论：%s", want, handoff)
		}
	}
	if strings.Contains(handoff, "通过时不要改任务状态；打回") {
		t.Fatalf("本轮审查说明仍把通过排除在提交之外：%s", handoff)
	}
}
