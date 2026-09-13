package agent

// 文件职责：ACP 家族 backend 共用的 token 用量"度量口径调和"层。
//
// ACP 运行时通过两条路径上报用量：进行中逐条累计的 usage_update 通知快照，与
// prompt 结束时终局响应里的 usage。两者都可能不全（runtime 时常只报部分桶、甚至
// 显式报 0），字段是否出现必须与值分开记录（见 acpUsageSnapshot.fields），否则
// 无法区分"上报了 0"与"根本没报"。本文件用"逐桶取最大 + 专门处理 input"的方式把
// 两条路径归并成一份确定结果，与 usage_update 到的早/晚无关。
//
// input 桶特殊：不同 runtime 对"inputTokens 是否已含缓存命中"口径不一，Grok Build
// 明确含在内（totalTokens == inputTokens + outputTokens）。因此把"已归一化的自述"
// 与"口径不明的候选"分开保存，最后从整个观测集合解析；归一化见
// normalizeACPTokenUsage——只在该恒等式成立时才把缓存前缀从 input 里拆出来，避免
// 仪表盘按桶算钱时把命中部分算两遍。

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// acpUsageFields 用位掩码标记"本次上报里哪些桶真的出现过"。
type acpUsageFields uint8

const (
	acpUsageInput      acpUsageFields = 1 << iota // 出现过 inputTokens
	acpUsageOutput                                // 出现过 outputTokens
	acpUsageCacheRead                             // 出现过缓存读入
	acpUsageCacheWrite                            // 出现过缓存写入
	acpUsageCost                                  // 出现过成本（provider 自述）
)

// acpUsageSnapshot 把"字段是否出现"与"解析出来的值"分开记录。prompt 的用量结果
// 经常是残缺的，仅凭零值无法区分"runtime 显式报了 0"还是"整个桶都没给"。
type acpUsageSnapshot struct {
	TokenUsage
	rawInputTokens  int64 // 未归一化的原始 input（见 normalizeInput）
	fields          acpUsageFields
	totalTokens     int64 // 终局响应里带的总量（用于判断 input 是否已含缓存）
	hasTotalTokens  bool
	inputNormalized bool // input 是否已按"input 含缓存"口径做过归一化
}

func (s acpUsageSnapshot) has(field acpUsageFields) bool {
	return s.fields&field != 0
}

// acpUsageAccumulator 把 ACP backend 共享的两条计量路径——累计的 usage_update
// 快照与终局 prompt 用量——调和成一份结果。
//
// 逐桶取最大既能去重"等价快照"，又保留某条路径里缺失的桶。input 特殊：把"自述已
// 归一化"与"口径不明"的候选分开保留，最后再从整个观测集合解析。这让结果不依赖
// 于迟到的 usage_update 到底先于还是后于终局 prompt 响应到达。
type acpUsageAccumulator struct {
	TokenUsage
	fields acpUsageFields

	ambiguousInput    int64 // 口径不明的 input 候选（可能是含缓存的原始数）
	hasAmbiguousInput bool
	normalizedInput   int64 // 自述已归一化的 input（对应 totalTokens == input+output 的形态）
	normalizedTotal   int64
	hasNormalized     bool
}

// merge 吸收一条快照；input 的归一化/不明两路先各自保留，逐桶取最大后统一解析。
func (a *acpUsageAccumulator) merge(next acpUsageSnapshot) {
	if next.has(acpUsageInput) {
		if next.inputNormalized {
			// 归一化候选：total 更大、或 total 相同而 input 更大的快照更新。
			if !a.hasNormalized ||
				next.totalTokens > a.normalizedTotal ||
				(next.totalTokens == a.normalizedTotal && next.InputTokens > a.normalizedInput) {
				a.normalizedInput = next.InputTokens
				a.normalizedTotal = next.totalTokens
				a.hasNormalized = true
			}
		} else if !a.hasAmbiguousInput || next.InputTokens > a.ambiguousInput {
			a.ambiguousInput = next.InputTokens
			a.hasAmbiguousInput = true
		}
	}
	if next.has(acpUsageOutput) && (!a.has(acpUsageOutput) || next.OutputTokens > a.OutputTokens) {
		a.OutputTokens = next.OutputTokens
	}
	if next.has(acpUsageCacheRead) && (!a.has(acpUsageCacheRead) || next.CacheReadTokens > a.CacheReadTokens) {
		a.CacheReadTokens = next.CacheReadTokens
	}
	if next.has(acpUsageCacheWrite) && (!a.has(acpUsageCacheWrite) || next.CacheWriteTokens > a.CacheWriteTokens) {
		a.CacheWriteTokens = next.CacheWriteTokens
	}
	// 对会上报它的 ACP backend，provider 成本是整个回合的累计总数。取最大既能
	// 保留"迟到/更全"的上报，又不会把同一条 prompt 结果和 usage_update 计两次费。
	if next.has(acpUsageCost) && (!a.has(acpUsageCost) || next.CostUSDTicks > a.CostUSDTicks) {
		a.CostUSDTicks = next.CostUSDTicks
	}

	a.fields |= next.fields
	a.resolveInput()
}

func (a acpUsageAccumulator) has(field acpUsageFields) bool {
	return a.fields&field != 0
}

// resolveInput 综合归一化/不明两路候选，定下最终 InputTokens。两路都有时，用
// 一条保守的累计下界（ambiguous input + output）判断归一化记录到底是一份累计
// 全量还是一份单次调用增量。
func (a *acpUsageAccumulator) resolveInput() {
	switch {
	case !a.hasAmbiguousInput && a.hasNormalized:
		a.InputTokens = a.normalizedInput
	case a.hasAmbiguousInput && !a.hasNormalized:
		a.InputTokens = a.ambiguousInput
	case a.hasAmbiguousInput && a.hasNormalized:
		// "input + output" 是一条保守的累计下界，即便 input 里含缓存命中也是。
		// 低于这条下界的归一化记录是单次调用增量，不能拿它顶替更大的累计流。
		if a.normalizedTotal >= a.ambiguousInput+a.OutputTokens {
			a.InputTokens = a.normalizedInput
		} else {
			a.InputTokens = a.ambiguousInput
		}
	}
}

// withFallback 用后备表示填上"首选表示里缺失（或报了 0）"的字段。ACP 的标准顶层
// usage 对厂商 `_meta` 有权威，嵌套 `_meta.usage` 又对它的扁平镜像有权威。
// provider 成本仍取最大，因为 0 表示"未上报"，而运行时可能只在某一种表示里给出
// 计价后的总数。
func (s acpUsageSnapshot) withFallback(fallback acpUsageSnapshot) acpUsageSnapshot {
	result := s
	inputFromFallback := false
	if fallback.has(acpUsageInput) && (!result.has(acpUsageInput) || result.rawInputTokens == 0) {
		result.InputTokens = fallback.InputTokens
		result.rawInputTokens = fallback.rawInputTokens
		result.inputNormalized = fallback.inputNormalized
		inputFromFallback = true
	}
	if fallback.has(acpUsageOutput) && (!result.has(acpUsageOutput) || result.OutputTokens == 0) {
		result.OutputTokens = fallback.OutputTokens
	}
	if fallback.has(acpUsageCacheRead) && (!result.has(acpUsageCacheRead) || result.CacheReadTokens == 0) {
		result.CacheReadTokens = fallback.CacheReadTokens
	}
	if fallback.has(acpUsageCacheWrite) && (!result.has(acpUsageCacheWrite) || result.CacheWriteTokens == 0) {
		result.CacheWriteTokens = fallback.CacheWriteTokens
	}
	if fallback.has(acpUsageCost) && (!result.has(acpUsageCost) || fallback.CostUSDTicks > result.CostUSDTicks) {
		result.CostUSDTicks = fallback.CostUSDTicks
	}

	result.fields |= fallback.fields
	if inputFromFallback && fallback.inputNormalized {
		result.totalTokens = fallback.totalTokens
		result.hasTotalTokens = fallback.hasTotalTokens
	} else if !result.hasTotalTokens && fallback.hasTotalTokens {
		result.totalTokens = fallback.totalTokens
		result.hasTotalTokens = true
	}
	result.normalizeInput()
	return result
}

// normalizeInput 总是从 provider 的原始 input 数出发做归一化，这样"首选/后备
// 合并"可以在所有互补字段齐全后再归一化，不会把缓存命中减两遍。
func (s *acpUsageSnapshot) normalizeInput() {
	s.InputTokens = s.rawInputTokens
	s.inputNormalized = false
	if !s.has(acpUsageInput) || !s.has(acpUsageOutput) || !s.has(acpUsageCacheRead) {
		return
	}
	s.TokenUsage, s.inputNormalized = normalizeACPTokenUsage(s.TokenUsage, s.totalTokens)
}

// parseACPTokenUsage 解析一段 ACP usage JSON（常见字段的别名都认），只取用量值。
func parseACPTokenUsage(data json.RawMessage) TokenUsage {
	return parseACPTokenUsageSnapshot(data).TokenUsage
}

// parseACPTokenUsageSnapshot 把一段 usage JSON 解析成快照。null/空/解析失败返回
// 空快照（调用方据此判断"这条上报没有用量"）。
func parseACPTokenUsageSnapshot(data json.RawMessage) acpUsageSnapshot {
	if len(data) == 0 || string(data) == "null" {
		return acpUsageSnapshot{}
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return acpUsageSnapshot{}
	}

	var snapshot acpUsageSnapshot
	if value, ok := acpUsageInt64(rawFields, "inputTokens", "input_tokens"); ok {
		snapshot.InputTokens = value
		snapshot.rawInputTokens = value
		snapshot.fields |= acpUsageInput
	}
	if value, ok := acpUsageInt64(rawFields, "outputTokens", "output_tokens"); ok {
		snapshot.OutputTokens = value
		snapshot.fields |= acpUsageOutput
	}
	// 缓存读入的别名最多：各家 runtime 叫法不一，全部兼容。
	if value, ok := acpUsageInt64(rawFields,
		"cachedReadTokens",
		"cacheReadTokens",
		"cached_input_tokens",
		"cache_read_tokens",
		"cache_read_input_tokens",
	); ok {
		snapshot.CacheReadTokens = value
		snapshot.fields |= acpUsageCacheRead
	}
	if value, ok := acpUsageInt64(rawFields,
		"cachedWriteTokens",
		"cacheWriteTokens",
		"cache_write_tokens",
		"cache_creation_input_tokens",
	); ok {
		snapshot.CacheWriteTokens = value
		snapshot.fields |= acpUsageCacheWrite
	}
	if value, ok := acpUsageInt64(rawFields, "costUsdTicks", "cost_usd_ticks"); ok {
		snapshot.CostUSDTicks = value
		snapshot.fields |= acpUsageCost
	}
	if value, ok := acpUsageInt64(rawFields, "totalTokens", "total_tokens"); ok {
		snapshot.totalTokens = value
		snapshot.hasTotalTokens = true
	}

	snapshot.normalizeInput()
	return snapshot
}

// parseACPTokenUsageSnapshotFromMeta 从 ACP 结果 `_meta` 对象里提取并调和用量。
// Grok Build 会同时给嵌套 `_meta.usage` 与扁平镜像字段；合并能补上两者各自缺失的
// 字段，又不至于把重复值算两遍。
func parseACPTokenUsageSnapshotFromMeta(meta json.RawMessage) acpUsageSnapshot {
	if len(meta) == 0 || string(meta) == "null" {
		return acpUsageSnapshot{}
	}

	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	var preferred acpUsageSnapshot
	if err := json.Unmarshal(meta, &envelope); err == nil &&
		len(envelope.Usage) > 0 && string(envelope.Usage) != "null" {
		// 嵌套 _meta.usage 是权威形态，扁平镜像作为它的后备。
		preferred = parseACPTokenUsageSnapshot(envelope.Usage)
	}
	return preferred.withFallback(parseACPTokenUsageSnapshot(meta))
}

// parseACPTokenUsageFromMeta 解析 _meta 里的用量并只取用量值。
func parseACPTokenUsageFromMeta(meta json.RawMessage) TokenUsage {
	return parseACPTokenUsageSnapshotFromMeta(meta).TokenUsage
}

// normalizeACPTokenUsage 把一条"inputTokens 里已含缓存命中"的用量记录重新分桶，
// 让落库的各桶互斥，仪表盘算成本时不会把缓存前缀收两遍。
//
// ACP 没规定缓存命中是否计入 inputTokens。Grok Build 计入：totalTokens ==
// inputTokens + outputTokens。只有当 totalTokens 证明了这种形态时才重新分桶；
// 上报互斥桶、或没给 totalTokens 的 agent 保持原样。
func normalizeACPTokenUsage(usage TokenUsage, totalTokens int64) (TokenUsage, bool) {
	if totalTokens <= 0 || usage.CacheReadTokens <= 0 || usage.CacheReadTokens > usage.InputTokens {
		return usage, false
	}
	if totalTokens != usage.InputTokens+usage.OutputTokens {
		return usage, false
	}
	usage.InputTokens -= usage.CacheReadTokens
	return usage, true
}

// excludeACPCachedInput 返回"已把缓存命中从 input 里剔除"的用量（能归一化才剔除，
// 否则原样）。
func excludeACPCachedInput(usage TokenUsage, totalTokens int64) TokenUsage {
	normalized, _ := normalizeACPTokenUsage(usage, totalTokens)
	return normalized
}

// acpUsageInt64 在一组候选字段名里按序找第一个能解析成非负整数（数字或数字字符串
// 都行，容忍小数）的字段。
func acpUsageInt64(fields map[string]json.RawMessage, names ...string) (int64, bool) {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		// 先按 json.Number（不开 UseNumber 会先把大数转成 float64）解析整数。
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&n); err == nil {
			if value, err := n.Int64(); err == nil && value >= 0 {
				return value, true
			}
			if value, err := n.Float64(); err == nil && value >= 0 {
				return int64(value), true
			}
		}
		// 少数 runtime 把数字当字符串发，比如 "123"。
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if value, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && value >= 0 {
				return value, true
			}
		}
	}
	return 0, false
}
