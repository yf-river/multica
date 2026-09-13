package agent

// 文件职责：内置运行时身份（built-in runtime identity）的注册表与构造入口——
// 那些"不在 SupportedTypes 里的协议族衍生体"（如 omp 之于 pi）。daemon 对每个
// 内置运行时独立探测并注册，再派发给它所属协议族的 backend。
//
// 关键解耦：描述符（BuiltinRuntime）是运行时身份的单一声明点——agents_probe.go、
// config.go、daemon.go 的显示名覆盖、execenv 的技能/配置路径、local_skills.go、
// ListModels 与前端显示都由它派生。给既有运行时加一个兼容分支 = 加一条描述符，
// 而不是动一整条链路。本文件还定义了 NewRuntime（构造内置运行时后端）与
// ResolveBackend（按 provider key 路由到 New 或 NewRuntime 的唯一生产入口）。

import (
	"context"
	"fmt"
)

// BuiltinRuntime 描述一个 daemon 会自动探测并注册的内置运行时身份——它独立于
// 自定义运行时 profile 的 protocol_family 白名单。多个运行时身份可以共享一个协议族
// （例如 "pi" 与 "omp" 都用 "pi" 协议后端）。
//
// 描述符是运行时身份的唯一声明点：agents_probe.go、config.go、daemon.go 的显示名
// 覆盖、execenv 技能/配置路径、local_skills.go、ListModels 与前端显示映射都由它
// 派生。给既有运行时加一个兼容分支，是加一条描述符条目，而不是动整个链路。
type BuiltinRuntime struct {
	// ID 是 daemon 注册时用的 provider key（如 "omp"）。它*不是* protocol_family
	// ——不会出现在 SupportedTypes 或 runtime_profile.protocol_family 的 CHECK
	// 约束里。
	ID string

	// ProtocolFamily 是这个运行时派发到的执行后端。它必须在 SupportedTypes 里。
	// NewRuntime 用 New() 构建该族的 backend，再把描述符按 ID 的专属默认值
	// （可执行文件、标签）套上去。
	ProtocolFamily string

	// DefaultCommand 是未设 MULTICA_<ID>_PATH 时、探测程序在 PATH 上找的裸 CLI
	// 名（如 "omp"）。
	DefaultCommand string

	// EnvPrefix 是 *_PATH 与 *_MODEL 环境变量覆盖的 MULTICA_ 前缀
	// （如 "MULTICA_OMP" → MULTICA_OMP_PATH / MULTICA_OMP_MODEL）。
	EnvPrefix string

	// DisplayName 是面向人类的运行时名。daemon 与前端都用它，保证永不漂移。
	DisplayName string

	// SkillsDir 是相对工作目录的项目级技能目录（如 ".omp/skills"）。
	SkillsDir string

	// UserSkillsDir 是相对 $HOME 的用户级技能目录（如 ".omp/agent/skills"）。
	UserSkillsDir string

	// LaunchHeader 是 UI 里展示的用户可见启动骨架（如 "omp (json mode)"）。
	LaunchHeader string

	// DefaultExecutable 是 cfg.ExecutablePath 为空时 backend 回退用的二进制名
	// （传给 piBackend.defaultExecutable）。
	DefaultExecutable string

	// ProviderLabel 是日志/错误消息里用的标签（传给 piBackend.providerLabel）。
	ProviderLabel string

	// ModelDiscovery 是发现可用模型的策略。设了它，就整个替换协议族的发现——
	// omp 用 `omp models --json`，命令与输出形态都和 pi 的 `--list-models` 不同。
	// 为 nil 时 ListModels 返回空目录而不是回退到协议族的命令：跑一条语义不兼容
	// 的命令（omp 遇到 `--list-models` 会非零退出）比降级到手动录入更糟。
	ModelDiscovery ModelDiscoveryFunc
}

// ModelDiscoveryFunc 为某运行时身份发现可用模型。它拿到 context 与解析后的
// 命令（可执行文件 + 运行时启动前缀），返回模型目录。二进制缺失或太旧时返回空
// 切片（ListModels 吞掉错误、降级到手动录入）。
type ModelDiscoveryFunc func(ctx context.Context, runtimeCmd Command) ([]Model, error)

// BuiltinRuntimes 是"不在 SupportedTypes 里"的内置运行时身份注册表（它们是
// 协议族衍生体，本身不是族）。daemon 逐个独立探测，再派发到其协议族的 backend。
//
// 一个运行时身份在"需要自己的探测/展示/技能、却复用既有 backend 协议"时出现在
// 这里。第一个条目是 omp（oh-my-pi）：一个说 pi JSON 事件协议的独立 CLI。
var BuiltinRuntimes = []BuiltinRuntime{
	{
		ID:                "omp",
		ProtocolFamily:    "pi",
		DefaultCommand:    "omp",
		EnvPrefix:         "MULTICA_OMP",
		DisplayName:       "Oh-My-Pi",
		SkillsDir:         ".omp/skills",
		UserSkillsDir:     ".omp/agent/skills",
		LaunchHeader:      "omp (json mode)",
		DefaultExecutable: "omp",
		ProviderLabel:     "omp",
		ModelDiscovery:    discoverOmpModels,
	},
}

// BuiltinRuntimeByID 返回给定运行时身份的描述符；不存在则返回 false。
func BuiltinRuntimeByID(id string) (BuiltinRuntime, bool) {
	for _, r := range BuiltinRuntimes {
		if r.ID == id {
			return r, true
		}
	}
	return BuiltinRuntime{}, false
}

// IsBuiltinRuntime 报告 id 是否已注册的内置运行时身份（区别于 SupportedTypes
// 里的协议族）。
func IsBuiltinRuntime(id string) bool {
	_, ok := BuiltinRuntimeByID(id)
	return ok
}

// BuiltinRuntimeCommands 返回全部内置运行时的默认 CLI 命令名，供 daemon 的
// defaultAgentCommandNames 列表使用。
func BuiltinRuntimeCommands() []string {
	cmds := make([]string, len(BuiltinRuntimes))
	for i, r := range BuiltinRuntimes {
		cmds[i] = r.DefaultCommand
	}
	return cmds
}

// backendOverrideApplicator 是让描述符把"按运行时覆盖项"（可执行文件、标签）套到
// 其协议族返回的 backend 上的接口。若某个族的 backend 没实现它，这个族就完全无法
// 承载运行时身份：NewRuntime 保守失败，而不是还回一个"覆盖项被悄悄丢掉"的
// backend。
type backendOverrideApplicator interface {
	applyBuiltinRuntimeOverrides(desc BuiltinRuntime)
}

// piBackend 已通过它的 defaultExecutable 与 providerLabel 字段实现了该接口。
// 方法定义在这里（而不是 pi.go），是为了让"描述符→backend 契约"集中在一个文件里。
func (b *piBackend) applyBuiltinRuntimeOverrides(desc BuiltinRuntime) {
	b.defaultExecutable = desc.DefaultExecutable
	b.providerLabel = desc.ProviderLabel
}

// ResolveBackend 是 daemon 用来按 provider key 构造 backend 的唯一生产入口。
// 它把内置运行时身份（如 "omp"）路由到 NewRuntime，把协议族（如 "pi"、"claude"）
// 路由到 New。这让两个工厂各只有一个含义：New() 只服务协议族，NewRuntime() 只服务
// 运行时身份，daemon 永远不用关心二者区别。
func ResolveBackend(provider string, cfg Config) (Backend, error) {
	if IsBuiltinRuntime(provider) {
		return NewRuntime(provider, cfg)
	}
	return New(provider, cfg)
}

// NewRuntime 为内置运行时身份（如 "omp"）创建 Backend。它先用 New() 构建描述符
// 协议族的 backend，再经 backendOverrideApplicator 接口套上按运行时的覆盖项。
// 这是运行时身份的生产入口；New() 只服务协议族、会拒收运行时 id。
//
// 保守失败：若协议族 backend 没实现 backendOverrideApplicator，描述符的覆盖项
// 就无法应用，运行时会在错误的可执行文件与标签下启动——所以 NewRuntime 返回错误，
// 而不是还回一个未修饰的 backend。
func NewRuntime(runtimeID string, cfg Config) (Backend, error) {
	desc, ok := BuiltinRuntimeByID(runtimeID)
	if !ok {
		return nil, fmt.Errorf("unknown runtime identity: %q", runtimeID)
	}
	cfg.provider = runtimeID
	backend, err := New(desc.ProtocolFamily, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtime %q (family %q): %w", runtimeID, desc.ProtocolFamily, err)
	}
	applicator, ok := backend.(backendOverrideApplicator)
	if !ok {
		return nil, fmt.Errorf("runtime %q: protocol family %q backend %T does not support runtime overrides", runtimeID, desc.ProtocolFamily, backend)
	}
	applicator.applyBuiltinRuntimeOverrides(desc)
	return backend, nil
}
