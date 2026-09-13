// 文件职责：识别「残留的 daemon 任务标记文件」——当 CLI 判定自己处在
// daemon 管理任务里、而唯一依据是一个其实可能残留的标记文件时，给各拒绝路径
// 提供统一文案（leftoverMarkerSuffix）。CLI 只提示文件名、绝不代删：删标记等于
// 拆掉一道 fail-closed 守卫，任何 CLI 侧检查都无法证明删除安全（见函数 doc）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

// leftoverDaemonTaskMarkerPath 返回「工作目录标记路径」——但仅当标记是 CLI 认为
// 自己身处 daemon 任务里的唯一理由、且该标记确实可能是残留时才返回；否则返回 ""。
//
// 两种情况会让标记失去「残留」待遇：
//
// 环境里任何 MULTICA_* 任务身份都说明 daemon 确实启动了本进程，标记在做它该做
// 的事。MULTICA_DAEMON_PORT 也算——虽然它单独不足以构成身份，但它仍表明 daemon
// 环境在场，那不是残留标记的特征。
//
// 非任务作用域的标记是永久性的 workspaces root 标记，永不失效，绝不能描述成
// 可删。
func leftoverDaemonTaskMarkerPath() string {
	if inAgentExecutionContext() ||
		strings.TrimSpace(os.Getenv(cli.TaskConfigRootEnv)) != "" ||
		strings.TrimSpace(os.Getenv("MULTICA_DAEMON_PORT")) != "" {
		return ""
	}
	markerPath := daemonTaskContextMarkerPath()
	if markerPath == "" || !markerIsTaskScoped(markerPath) {
		return ""
	}
	return markerPath
}

// markerIsTaskScoped 判断 path 处的标记是否属于某个具体任务，而非 workspaces root
// 标记。
//
// 两类标记共用同一文件名，daemonTaskContextMarkerPath 只靠 managed_by 区分它们。
// 任务级标记带写入它的任务身份、随任务消亡；workspaces root 标记不带任何身份：
// daemon 每次启动都会重建它，作为节点级的永久守卫，让逃逸出工作目录的子进程也
// 能失败关闭。让任何人（用户、或丢了环境正在读报错的 agent 子进程）去删后者，
// 等于拆掉它存在的意义，所以对 root 标记给出干脆的拒绝。
//
// 「身份」指 agent_id 或 issue_id 二选一：聊天任务没有 issue，若强行要求两者都带，
// 恰好会把可能出现残留的地方误判成 root 标记、丢掉提示。root 标记两个字段都没有，
// 任一个在场就足够区分。
func markerIsTaskScoped(markerPath string) bool {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	var marker struct {
		AgentID string `json:"agent_id"`
		IssueID string `json:"issue_id"`
	}
	if json.Unmarshal(data, &marker) != nil {
		return false
	}
	return strings.TrimSpace(marker.AgentID) != "" || strings.TrimSpace(marker.IssueID) != ""
}

// leftoverMarkerSuffix 渲染所有拒绝路径在「任务级工作目录标记是唯一 daemon 信号」
// 时统一追加的文案尾巴：
//
//   - newAPIClient（API 命令）
//   - requireHumanLocalCommand（本地 daemon/profile 命令）
//   - requireTaskLocalConfigRoot（config show/set、auth status）
//
// 三条拒绝路径成因与解法相同，所以说同样的话。把措辞收在一处才能防止它们再次
// 漂移——MUL-6132 之前只有 API 路径指名道姓给出文件名，用户能否照着报错行动，
// 取决于他碰巧先跑了哪条命令（MUL-6132 评审）。
//
// CLI 刻意止步于「说出文件名」而不代为删除。删标记等于拆掉一道失败关闭的守卫，
// CLI 能做的检查没有一个能证明删除安全：daemon 只在 claim 返回之后才递增活跃任务
// 计数，所以探测可能读到 0——而该任务其实已被认领、正要写这个标记。因此退役动作
// 属于 daemon，它能在自己的 claim 路径上做串行化。
func leftoverMarkerSuffix(markerPath string) string {
	return fmt.Sprintf("; detected a daemon task marker at %s — if you are not running inside an agent task this is likely a leftover, remove it and retry", markerPath)
}
