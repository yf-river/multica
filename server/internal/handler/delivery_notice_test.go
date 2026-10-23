package handler

import (
	"strings"
	"testing"
)

// 发布结果通知是 Agent 收尾时最贴近的一条指令：已发布要能自行收尾，失败时不能把任务说成完成。
func TestDeliveryNoticeTextKeepsCompletionAndFailureApart(t *testing.T) {
	published := deliveryNoticeText("published", "delivery-1", "candidate-1", "")
	for _, want := range []string{
		"生产发布已完成",
		"生产验证通过后直接标记任务完成",
		"无需再次等待用户确认",
		"验证未通过或发布异常时不要标记完成",
	} {
		if !strings.Contains(published, want) {
			t.Errorf("published notice missing %q: %s", want, published)
		}
	}
	if strings.Contains(published, "已授权关闭") {
		t.Errorf("published notice still asks for a separate close authorization: %s", published)
	}

	paused := deliveryNoticeText("needs_changes", "delivery-1", "candidate-1", "生产构建失败")
	for _, want := range []string{"暂停", "生产构建失败", "不要盲目重跑"} {
		if !strings.Contains(paused, want) {
			t.Errorf("paused notice missing %q: %s", want, paused)
		}
	}
	if strings.Contains(paused, "标记任务完成") {
		t.Errorf("paused notice must not tell the agent to close the issue: %s", paused)
	}
}
