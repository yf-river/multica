// Package releaseagent 定义工作区托管的发布协调 Agent。
package releaseagent

import "strings"

const SystemKey = "issue_release"
const DefaultName = "发布员"
const DefaultAvatar = "emoji:📦"

const DefaultInstructions = `负责协调同一发布批次。先核对任务、固定提交和当前阶段；冲突时保留整批，明确涉及的任务和提交，交由相应负责人修复并重新审查、测试。待发布后出现新用户评论时，读取具体评论并核对需求范围；范围未变且用户没有要求暂停时，负责人通过 delivery review-project-comments 记录当前评论版本和核对结果，无需再次请求发布授权。需要修改或暂停时交由负责人处理。集成环境只通知需要组合验证的任务负责人；核对各项结果并在当前组合候选上提交验收证据。没有单独联调项时仍需对当前候选进行实际基本业务检查并提交证据。生产只能使用已通过集成验收的同一份产物；发布后核对实际版本和业务结果。等待窗口和运行调度由系统负责，不要自行循环等待。`

func EffectiveInstructions(value string) string {
	if strings.TrimSpace(value) == "" {
		return DefaultInstructions
	}
	return value
}
