package agent

// 文件职责：把 ACP 回合的文本流切分成"最终交付物"与"完整转写"两部分。
//
// Result.Output 的语义是"backend 挑出来的、面向用户的最终输出"——它会变成频道
// 回复和自动生成的 issue 评论，所以中间过程的旁白（"让我先查下日志……"）绝不能
// 混进去（#6006）。ACP 运行时把旁白和最终答案发成同一种 AgentMessageChunk，
// 工具调用是它们暴露的唯一分界线，因此"交付物 = 最近一次工具调用之后出现的文本"。
// 在运行时显式标注最终答案之前，这条分界线只是一个启发式。
//
// 完整转写也必须保留：provider 错误检测要读每一条 chunk——适配器会把终局的
// "API 调用重试 N 次仍失败"放弃消息当作普通 agent 回合注入，它可能落在一次工具
// 调用之前，单独看交付物根本看不见。
//
// 零值即可直接使用。并发安全：backend 从 ACP stdout 读取协程调 observe，会话
// 协程调 result。

import (
	"strings"
	"sync"
)

type acpDeliverableTracker struct {
	mu sync.Mutex
	// full 是该回合所有文本块，按到达顺序。
	full strings.Builder
	// deliverable 是自最近一次工具调用以来累积的文本。
	deliverable strings.Builder
	// lastTextBlock 是被工具调用打断的最近一段非空文本；当回合以工具调用收尾、
	// 没有结尾文本块时用它兜底。
	lastTextBlock string
}

// observe 记录一条流式消息。backend 把本回合收下的每条消息都传进来；除文本与
// 工具调用之外的消息类型不携带交付信号，直接忽略。
func (t *acpDeliverableTracker) observe(msg Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch msg.Type {
	case MessageText:
		t.full.WriteString(msg.Content)
		t.deliverable.WriteString(msg.Content)
	case MessageToolUse:
		// 工具调用是分界线：先留住当前段文本作兜底，再清空累积器。
		if block := t.deliverable.String(); strings.TrimSpace(block) != "" {
			t.lastTextBlock = block
		}
		t.deliverable.Reset()
	}
}

// result 返回交付物（供 Result.Output）与完整文本流（供扫描 provider 错误）。
// 若回合以工具调用收尾、没有结尾文本块，交付物就回退到最近一段非空文本，而不是
// 交出一段空回复。
func (t *acpDeliverableTracker) result() (deliverable, full string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	deliverable = t.deliverable.String()
	if strings.TrimSpace(deliverable) == "" {
		deliverable = t.lastTextBlock
	}
	return deliverable, t.full.String()
}
