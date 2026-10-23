// Package review 定义「进入审核中自动派审查 Agent」所用的托管身份、默认提示词与停止策略。
// 审查配置与轮次状态分别存在 review_config / issue_review 两张表，这里只放与存储无关的常量。
package review

import (
	_ "embed"
	"sort"
	"strings"
)

const (
	// SystemKey 标记工作区托管的审查 Agent。它由审查设置自动创建与维护，
	// 与 Mika 一样保持 kind='user' 以保留运行历史，但靠 system_key 从
	// Agent 列表、指派与调用面隐藏。
	SystemKey = "issue_review"
	// DefaultName 是托管审查 Agent 的显示名，设置页改名不生效。
	DefaultName = "审查员"
	// 1. Managed review agents cannot use the generic avatar upload path, so the
	// reviewer is created with a fixed avatar; otherwise comments and review
	// badges render without one.
	DefaultAvatar = "emoji:🧐"
	// MaxRounds 是单个 issue 连续审查轮次上限。正常情况下打回会带来实质改动，
	// 这只是一道成本闸门，避免两个 Agent 无限对撞。
	MaxRounds = 6
)

// 2. Rows created before the default avatar existed store no value, and the
// managed identity cannot use the avatar upload path, so reads fall back to the
// default. Backfilling those rows would need a data migration, which the release
// gate only accepts for reviewed statements.
func AvatarURL(stored string) string {
	if strings.TrimSpace(stored) == "" {
		return DefaultAvatar
	}
	return stored
}

// Verdict 是审查员对一轮审查给出的结论。
type Verdict string

const (
	// VerdictPass 表示通过：保留在审核中等用户确认，写 review_passed 标记。
	VerdictPass Verdict = "pass"
	// VerdictReject 表示必须改：状态回到进行中并唤醒原受理人。
	VerdictReject Verdict = "reject"
	// VerdictStalled 表示上一轮问题原样未动：不再自动来回，停下交给人判断。
	VerdictStalled Verdict = "stalled"
)

// RoundState 是 issue_review.state 的取值。
const (
	StateIdle       = "idle"
	StateDispatched = "dispatched"
	StateRejected   = "rejected"
	StatePassed     = "passed"
	StateStopped    = "stopped"
)

//go:embed default_instructions.md
var defaultInstructions string

// DefaultInstructions 返回内置审查提示词，设置页「恢复默认」与新建托管 Agent 都用它。
func DefaultInstructions() string {
	return strings.TrimSpace(defaultInstructions)
}

// EffectiveInstructions：用户自定义优先，空值跟随内置默认。
func EffectiveInstructions(custom string) string {
	if strings.TrimSpace(custom) == "" {
		return DefaultInstructions()
	}
	return custom
}

// Finding 是一条审查问题，落库后供下一轮判断是否真的改过。
type Finding struct {
	Level  string `json:"level"`
	File   string `json:"file,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Findings 是一轮审查的结构化结论。
type Findings struct {
	Summary           string    `json:"summary,omitempty"`
	Items             []Finding `json:"items,omitempty"`
	UnchangedPrevious []string  `json:"unchanged_previous,omitempty"`
}

// Normalize 清理空白并补全默认结论，避免上层对空值做分支判断。
func (f Findings) Normalize(verdict Verdict) Findings {
	f.Summary = strings.TrimSpace(f.Summary)
	items := make([]Finding, 0, len(f.Items))
	for _, item := range f.Items {
		item.Level = strings.ToLower(strings.TrimSpace(item.Level))
		item.File = strings.TrimSpace(item.File)
		item.Detail = strings.TrimSpace(item.Detail)
		if item.Level == "" || item.Detail == "" {
			continue
		}
		items = append(items, item)
	}
	f.Items = items
	f.UnchangedPrevious = compact(f.UnchangedPrevious)
	if f.Summary == "" {
		switch verdict {
		case VerdictPass:
			f.Summary = "审查通过"
		case VerdictStalled:
			f.Summary = "上一轮问题没有实质改动，停止自动返工"
		default:
			f.Summary = "发现需要修改的问题"
		}
	}
	return f
}

// ParseVerdict 把 CLI 输入归一化；未知值返回空串由调用方拒绝。
func ParseVerdict(raw string) Verdict {
	switch Verdict(strings.ToLower(strings.TrimSpace(raw))) {
	case VerdictPass:
		return VerdictPass
	case VerdictReject:
		return VerdictReject
	case VerdictStalled:
		return VerdictStalled
	default:
		return ""
	}
}

// StopReason 说明为什么停止自动返工；空串表示继续自动打回。
func StopReason(round int, verdict Verdict) string {
	if verdict == VerdictStalled {
		return "上一轮问题在最新提交里没有任何改动"
	}
	if round >= MaxRounds {
		return "累计审查轮次达到上限"
	}
	return ""
}

func compact(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
