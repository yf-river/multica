package agent

// 文件职责：给所有读取 agent 事件流的 backend 提供统一的 bufio.Scanner 构造。
//
// agent 传输多为"按行分隔的 JSON"，单行可能装下整段对话：Codex app-server 会把
// 一整条线程序列化进单个 thread/resume 响应，Pi 这类"每次 delta 重发增长中的消息
// 片段"的 provider、或把大段工具结果嵌进单个事件的做法也一样会撑大单行。所有
// backend 读行分隔传输都必须走本构造，否则单行上限会逐 backend 复制粘贴、静默
// 漂移——GH#4520 的修复当年就是这么只落到其中一个 backend 上的。

import (
	"bufio"
	"io"
)

// agentStreamMaxLineBytes 限定从 agent CLI 事件流读单行的上限。超过这个上限会让
// scanner.Scan() 以 bufio.ErrTooLong 返回 false，backend 只能把它报成传输故障——
// 会话本身没事，只是我们读不动了。
//
// 32 MiB 是 GH#4520 的后续修订，当时 10 MiB 的上限弄断了长 Codex 线程的
// thread/resume（MUL-5722）。这是余量、不是保证：线程仍可能超过任何固定上限，
// 所以"超限后的恢复路径"比这个数字本身更重要。
const agentStreamMaxLineBytes = 32 * 1024 * 1024

// agentStreamInitialBufferBytes 是 scanner 的初始分配。超出它的行仍然会长到
// agentStreamMaxLineBytes；这个值只决定首次增长前预留多少，普通事件因此不用
// 重新分配。
const agentStreamInitialBufferBytes = 1024 * 1024

// newAgentStreamScanner 返回按 agent 事件流尺寸调好的 bufio.Scanner。所有读取
// 行分隔 agent 传输的 backend 都必须经过这个构造函数：这个上限过去是各 backend
// 各抄一份、悄悄分叉——GH#4520 的修复就是这样只覆盖了其中一个。
func newAgentStreamScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, agentStreamInitialBufferBytes), agentStreamMaxLineBytes)
	return scanner
}
