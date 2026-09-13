package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// 文件导读：本文件决定并写入每个任务 Codex config.toml 里的沙箱策略块。
//
// 各平台背景不同：macOS 上 Codex Seatbelt 的 workspace-write 模式会静默忽略
// `[sandbox_workspace_write] network_access = true`（见 openai/codex#10390），必须回退
// 到 danger-full-access 让 agent 能访问 Multica API；Linux 上跑 danger-full-access 是
// 明确的产品决定而非兼容回退（隔离来自 daemon 自身运行所在边界）；Windows 上默认
// danger-full-access 是兼容选择，用户显式选 windows.sandbox 时保留 workspace-write，
// 无法判定时关闭即失败。本文件写出的受管块位于文件顶部、只用顶层点号键，避免把用户
// 内容错误地卷进 `[table]` 作用域。

// 背景
//
// macOS 上 Codex 的 Seatbelt 沙箱在 `workspace-write` 模式下会静默忽略
// `[sandbox_workspace_write] network_access = true`。DNS 解析在系统调用层被阻断，因此
// 沙箱内的进程向外呼叫时会看到 `no such host` 错误（例如 `multica issue get` 打
// Multica API）。见上游 issue openai/codex#10390。
//
// 在有修复的 Codex 版本发布之前，macOS 的 per-task Codex 配置必须回退到
// `sandbox_mode = "danger-full-access"`，让 agent 能真正连上 Multica API。
//
// Linux 跑 danger-full-access 是刻意的产品决定（MUL-5578、#6218），而非兼容回退——
// 见 codexSandboxPolicyFor。

// CodexDarwinNetworkAccessFixedVersion 是「macOS 上 Seatbelt 下 `network_access =
// true` 得到尊重」的最早 Codex CLI 版本。上游修复发布时请更新此常量。空字符串表示
// 「尚无已知修复版本——macOS 的 Codex 在网络访问上一律按坏的对待」。
const CodexDarwinNetworkAccessFixedVersion = ""

// codexSandboxPolicy 描述 per-task Codex config.toml 应如何配置沙箱。
type codexSandboxPolicy struct {
	// Mode 是写为 `sandbox_mode = "..."` 的值。
	Mode string
	// NetworkAccess 控制 `[sandbox_workspace_write] network_access`。
	// 仅 Mode == "workspace-write" 时有意义。
	NetworkAccess bool
	// Reason 是用于 warn 级日志的简短人类可读说明。
	Reason string
	// Hint 是 Mode 为 danger-full-access 时、在 warn 级日志里呈现的可操作修复建议。
	// 没有可呈现的通用动作时为空（例如 Windows 兼容回退——启用 Codex 原生沙箱属于
	// 后续跟进工作，而非版本升级），日志便会省略 hint 而不展示无关内容。
	Hint string
}

// resolveGOOS 返回 goos；goos 为空时返回 runtime.GOOS。测试调用方显式传 goos；
// 生产环境留空以使用宿主平台。
func resolveGOOS(goos string) string {
	if goos == "" {
		return runtime.GOOS
	}
	return goos
}

// codexSandboxPolicyFor 为给定平台与探测到的 Codex CLI 版本挑选默认策略。它是平台
// 基线；per-task 用户配置可以进一步细化它（见 codexSandboxPolicyForConfig）。
//
//   - Linux（以及任何其它非 darwin、非 windows 平台）：
//     danger-full-access，跑在 daemon 用户真实 HOME 上。Landlock 本可在这里强制
//     workspace-write，但那样做意味着每个任务都要重定向 HOME/XDG、并在其中播种一份
//     手工维护的凭据路径白名单——这会让每个没被列出的宿主 CLI（aws、kube、gcloud、
//     glab、rclone……）在任务里没配置，而且无法收敛。它买到的围栏也比看上去窄：
//     workspace-write 只限制写入——读与网络本就不受限，所以真实 HOME 下的凭据无论如何
//     都可读、可外传。因此任务的文件系统边界就是 daemon 自身运行所在的边界（VM、
//     容器或专用 Unix 用户），与 macOS/Windows 对齐。见 MUL-5578 / #6218，以及
//     apps/docs 的 security-model。
//   - Windows：danger-full-access，作为刻意的兼容选择。Codex 自带原生 Windows 沙箱
//     （经 Restricted Token 的 windows.sandbox = "unelevated"，或 "elevated"），但仍属
//     实验特性、存在已知可靠性限制，所以 daemon 默认不启用。未配置原生 windows.sandbox
//     时，Codex 在 Windows 上无法强制 workspace-write（会降级为只读），随后在
//     approval_policy = "never" 下按 "policy" 拒绝非安全变更命令——例如 `multica issue
//     create` 会失败——因为请求根本到不了 daemon 的自动批准器。danger-full-access
//     绕开它。启用原生沙箱另作跟进。见 MUL-4957。已选择 windows.sandbox（经
//     config.toml 或 `-c` 自定义参数）的用户保留 workspace-write 而非本回退；无法判定
//     的配置关闭即失败；逻辑在 codexSandboxPolicyForConfig /
//     resolveWindowsSandboxState。
//   - darwin 且版本不低于 CodexDarwinNetworkAccessFixedVersion：
//     workspace-write + 网络访问（上游 bug 已修复）。
//   - darwin 其余情况（含版本未知）：回退 danger-full-access，使 Multica CLI 能连上
//     API。
func codexSandboxPolicyFor(goos, detectedVersion string) codexSandboxPolicy {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		return codexSandboxPolicy{
			Mode:   "danger-full-access",
			Reason: "codex on windows: compatibility fallback; no native windows.sandbox configured, so workspace-write cannot be enforced (MUL-4957)",
		}
	}
	if goos != "darwin" {
		return codexSandboxPolicy{
			Mode:   "danger-full-access",
			Reason: "codex on " + goos + ": tasks run with the daemon user's real HOME and full filesystem access; isolation comes from the boundary the daemon runs inside (MUL-5578)",
			Hint:   codexLinuxIsolationHint(),
		}
	}
	if codexDarwinNetworkAccessFixed(detectedVersion) {
		return codexSandboxPolicy{
			Mode:          "workspace-write",
			NetworkAccess: true,
			Reason:        "codex version includes macOS network_access fix",
		}
	}
	reason := "codex on macOS: seatbelt ignores sandbox_workspace_write.network_access (openai/codex#10390)"
	if detectedVersion == "" {
		reason += " — version unknown, assuming broken"
	}
	return codexSandboxPolicy{
		Mode:          "danger-full-access",
		NetworkAccess: false,
		Reason:        reason,
		Hint:          codexUpgradeHint(),
	}
}

// windowsSandboxConfig 是原生 Codex Windows 沙箱选择的三态。做成三值（而非 bool）
// 是为了让「无法判定」的配置关闭即失败——daemon 无法确认用户意图时绝不放宽到
// danger-full-access。见 MUL-4957。
type windowsSandboxConfig int

const (
	// windowsSandboxAbsent：确定任何地方都没选原生沙箱，应用 danger-full-access
	// 兼容回退是安全的。
	windowsSandboxAbsent windowsSandboxConfig = iota
	// windowsSandboxNative：选了合法的 windows.sandbox = "unelevated"|"elevated"，
	// 保留 workspace-write 让 Codex 强制隔离。
	windowsSandboxNative
	// windowsSandboxUndecidable：配置读不出/解析不了，或带着 Codex 不接受的
	// windows.sandbox 值。daemon 无法判断用户是否要隔离，因此绝不能放宽——关闭即失败。
	windowsSandboxUndecidable
)

// codexSandboxPolicyForConfig 对 linux/darwin 返回 codexSandboxPolicyFor 的平台默认。
// Windows 上套用解析后的原生沙箱状态（见 resolveWindowsSandboxState）：选择
// windows.sandbox 的用户保留 workspace-write；无法判定的配置关闭即失败为
// workspace-write；只有「确定缺失」才拿 danger-full-access 兼容回退。见 MUL-4957。
//
// 这刻意作为将来原生沙箱上线的分支点：日后翻转 Windows 默认，只需自己写
// windows.sandbox 并把 winState 默认成 native，而无需重构调用方。
func codexSandboxPolicyForConfig(goos, detectedVersion string, winState windowsSandboxConfig) codexSandboxPolicy {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		return codexSandboxPolicyForWindows(winState)
	}
	return codexSandboxPolicyFor(goos, detectedVersion)
}

// codexSandboxPolicyForWindows 把解析后的原生沙箱状态映射成策略。Native 与
// Undecidable 都保留 workspace-write（Undecidable 是关闭即失败的情形——绝不因存疑而
// 放宽）；只有确定缺失的原生沙箱才拿 danger-full-access 兼容回退。
func codexSandboxPolicyForWindows(state windowsSandboxConfig) codexSandboxPolicy {
	switch state {
	case windowsSandboxNative:
		return codexSandboxPolicy{
			Mode:          "workspace-write",
			NetworkAccess: true,
			Reason:        "codex on windows: native windows.sandbox configured; keeping workspace-write so Codex enforces task isolation",
		}
	case windowsSandboxUndecidable:
		return codexSandboxPolicy{
			Mode:          "workspace-write",
			NetworkAccess: true,
			Reason:        "codex on windows: windows.sandbox config undecidable (unreadable/unparseable/invalid); failing closed to workspace-write rather than loosening (MUL-4957)",
		}
	default: // windowsSandboxAbsent
		return codexSandboxPolicy{
			Mode:   "danger-full-access",
			Reason: "codex on windows: compatibility fallback; no native windows.sandbox configured (MUL-4957)",
		}
	}
}

// windowsSandboxFromConfig 对一段 config.toml 正文里的 windows.sandbox 选择归类。
// Codex 只接受精确小写变体 "unelevated"/"elevated"，遇到任何其它值都拒绝加载配置，
// 因此其它存在值一律按「无法判定」（关闭即失败）处理，而不是安全的「缺失」。无法解析
// 的 TOML 同样无法判定——Codex 会拒绝同一文件。windows.sandbox 键缺席才是货真价实
// 的「缺失」。
func windowsSandboxFromConfig(config string) windowsSandboxConfig {
	var probe struct {
		Windows struct {
			Sandbox string `toml:"sandbox"`
		} `toml:"windows"`
	}
	if err := toml.Unmarshal([]byte(config), &probe); err != nil {
		return windowsSandboxUndecidable
	}
	return classifyWindowsSandboxValue(probe.Windows.Sandbox)
}

// codexWindowsSandboxOverrideRe 匹配 Codex `-c` / `--config` 的 windows.sandbox 覆盖
// 值的键 token，例如 `windows.sandbox = "unelevated"`。容忍点号键与 `=` 周围空白，
// 对齐 Codex 自己宽松的 `-c` 解析。
var codexWindowsSandboxOverrideRe = regexp.MustCompile(`^\s*windows\s*\.\s*sandbox\s*=`)

// windowsSandboxFromCustomArgs 对经 Codex `-c windows.sandbox=...` / `--config
// windows.sandbox=...` 参数传入的原生 Windows 沙箱选择归类。它们从不落进 config.toml
// （留在 argv、叠在其上生效），只看配置会漏掉它们——MUL-4957 review 的第二必修项。
// 镜像 server/pkg/agent 的 buildCodexArgs 里解析覆盖的形态：内联（`-c=windows.sandbox=x`）
// 与双 token（`-c windows.sandbox=x`）两种，最后一次出现生效（Codex 后写覆盖）。
func windowsSandboxFromCustomArgs(args []string) windowsSandboxConfig {
	state := windowsSandboxAbsent
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag := arg
		value := ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			value = arg[idx+1:]
			hasInlineValue = true
		}
		if flag != "-c" && flag != "--config" {
			continue
		}
		if !hasInlineValue {
			if i+1 >= len(args) {
				continue
			}
			i++
			value = args[i]
		}
		if !codexWindowsSandboxOverrideRe.MatchString(value) {
			continue
		}
		// 是 windows.sandbox 覆盖 token：取它首个 `=` 之后的部分。
		if eq := strings.Index(value, "="); eq >= 0 {
			state = classifyWindowsSandboxValue(value[eq+1:])
		}
	}
	return state
}

// classifyWindowsSandboxValue 把原始 windows.sandbox 值（来自 config.toml 或 `-c`
// 参数，可能带空白/引号）映射成三态。只有 Codex 接受的精确小写变体算原生；存在但
// 不被接受的值是无法判定（Codex 会拒绝该配置）；空值是缺失。
func classifyWindowsSandboxValue(raw string) windowsSandboxConfig {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, `"'`)
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return windowsSandboxAbsent
	case "unelevated", "elevated":
		return windowsSandboxNative
	default:
		return windowsSandboxUndecidable
	}
}

// resolveWindowsSandbox 把各层状态折叠成一个。Undecidable 胜过一切（任何一层坏掉/
// 歧义都意味着关闭即失败）；其次是 Native 胜过 Absent（任意一层的显式选择都保持
// 隔离）。
func resolveWindowsSandbox(states ...windowsSandboxConfig) windowsSandboxConfig {
	result := windowsSandboxAbsent
	for _, s := range states {
		if s == windowsSandboxUndecidable {
			return windowsSandboxUndecidable
		}
		if s == windowsSandboxNative {
			result = windowsSandboxNative
		}
	}
	return result
}

// codexDarwinNetworkAccessFixed 在给定探测版本「已知会尊重 macOS Seatbelt 下的
// `network_access = true`」时返回 true。
func codexDarwinNetworkAccessFixed(detectedVersion string) bool {
	if CodexDarwinNetworkAccessFixedVersion == "" || detectedVersion == "" {
		return false
	}
	fixed, err := parseCodexSemver(CodexDarwinNetworkAccessFixedVersion)
	if err != nil {
		return false
	}
	got, err := parseCodexSemver(detectedVersion)
	if err != nil {
		return false
	}
	return !got.lessThan(fixed)
}

// codexUpgradeHint 给跑在受 macOS network_access bug 影响版本上的用户返回简短、
// 可操作的提示。
func codexUpgradeHint() string {
	return "upgrade Codex CLI (e.g. `brew upgrade codex` or `npm i -g @openai/codex`) once a release including openai/codex#10390 is available to restore workspace-write + network_access"
}

// codexLinuxIsolationHint 返回 Linux full-access 默认值的可操作补救：与 macOS bug
// 不同，这里没有可升级到的版本——围栏必须来自 daemon 运行所在的边界。
func codexLinuxIsolationHint() string {
	return "run the daemon inside a VM, container, or dedicated Unix user — tasks can read and write everything that user can"
}

// multicaManagedBeginMarker / multicaManagedEndMarker 界定 daemon 写进 per-task
// config.toml 的受管块。标记之间的一切都归 daemon、会被幂等重写；标记之外的内容原样
// 保留。
const (
	multicaManagedBeginMarker = "# BEGIN multica-managed (do not edit; regenerated by daemon)"
	multicaManagedEndMarker   = "# END multica-managed"
)

// renderMulticaManagedBlock 为给定策略产出受管块。
//
// 块里只有顶层 key=value 赋值——没有 `[table]` 头——嵌套值用 TOML 点号键语法。
// 这很重要，因为块要插进用户自有的 config.toml：
//
//   - 若块开了 `[sandbox_workspace_write]` 头，恰好位于其下的任何用户内容都会被静默
//     改姓、归进该表。
//   - 若块被追加到「已在某张其它表里结尾」的文件末尾（如 `[permissions.multica]`），
//     一条裸 `sandbox_mode = ...` 会被解析成前一张表的子键。
//
// 让块保持纯顶层点号键赋值、并放在文件顶部（见 upsertMulticaManagedBlock），可同时
// 避开这两个坑。
func renderMulticaManagedBlock(policy codexSandboxPolicy) string {
	var b strings.Builder
	b.WriteString(multicaManagedBeginMarker)
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("sandbox_mode = %q\n", policy.Mode))
	if policy.Mode == "workspace-write" {
		b.WriteString(fmt.Sprintf("sandbox_workspace_write.network_access = %t\n", policy.NetworkAccess))
	}
	b.WriteString(multicaManagedEndMarker)
	b.WriteString("\n")
	return b.String()
}

// managedBlockRe 捕获 daemon 自有的块（含四周标记与尾部空行），以便幂等替换。
// 用 `\n*` 而非 `\n?`，使块与另一受管块（如 multi-agent）共存于文件时重跑不会累积
// 空行。
var managedBlockRe = regexp.MustCompile(
	`(?ms)^` + regexp.QuoteMeta(multicaManagedBeginMarker) +
		`.*?^` + regexp.QuoteMeta(multicaManagedEndMarker) + `\n*`)

// upsertMulticaManagedBlock 返回「受管块位于文件最顶部」的配置内容。任何先前写下的
// 受管块被原地移除；标记之外的用户内容原样保留。
//
// 块总是被提升到顶部（而非原地替换或追加到 EOF），使其顶层键总在 TOML 根被解析，
// 无论用户配置是否在某张表（如 `[permissions.multica]`、`[profiles.foo]`）内部结尾。
// 配合 renderMulticaManagedBlock 的点号键形式，受管块既不会漏进任何外围表作用域、
// 也不会继承它。
func upsertMulticaManagedBlock(content string, policy codexSandboxPolicy) string {
	// 移除任何先前写下的受管块（无论它在哪）。
	content = managedBlockRe.ReplaceAllString(content, "")
	block := renderMulticaManagedBlock(policy)
	// 去掉移除后留下的前导空行，别让每次幂等重写都把文件撑大。
	content = strings.TrimLeft(content, "\n")
	if content == "" {
		return block
	}
	return block + "\n" + content
}

// stripLegacySandboxDirectives 移除会与受管块冲突的顶层 `sandbox_mode = ...` 行与任何
// `[sandbox_workspace_write]` 段。这让 daemon 能迁移「由更早 daemon 写成、把那些值内联
// 写入」的任务 config.toml。
//
// 只剥顶层条目；无关段头（如 `[permissions.foo]`）下的一切原样保留。
func stripLegacySandboxDirectives(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inLegacyWorkspaceWrite := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// 进入新段。若先前在旧式段内则退出追踪。
			inLegacyWorkspaceWrite = trimmed == "[sandbox_workspace_write]"
			if inLegacyWorkspaceWrite {
				continue
			}
			out = append(out, line)
			continue
		}
		if inLegacyWorkspaceWrite {
			// 丢弃旧式段体，直到下一段。
			continue
		}
		if strings.HasPrefix(trimmed, "sandbox_mode") {
			// 丢弃旧式顶层 sandbox_mode 声明。
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ensureCodexSandboxConfig 按策略把 multica 管理的 sandbox 块写进给定 config.toml。
// 幂等：连跑两次产出相同文件内容。文件不存在则创建。
//
// 只要解析出的模式是 danger-full-access——Linux 默认、macOS seatbelt 回退、Windows
// 无原生沙箱回退都是——本函数就记 warn 日志，使每个无沙箱任务在 daemon 日志里可见。
func ensureCodexSandboxConfig(configPath string, policy codexSandboxPolicy, detectedVersion string, logger *slog.Logger) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	existing := string(data)

	// 剥掉更老 daemon 版本留下的内联 sandbox_mode / [sandbox_workspace_write]，免得
	// 它们与受管块撞车。
	if existing != "" && !managedBlockRe.MatchString(existing) {
		existing = stripLegacySandboxDirectives(existing)
	}

	updated := upsertMulticaManagedBlock(existing, policy)
	if updated == string(data) {
		return nil
	}

	if policy.Mode == "danger-full-access" && logger != nil {
		version := detectedVersion
		if version == "" {
			version = "unknown"
		}
		attrs := []any{
			"reason", policy.Reason,
			"codex_version", version,
			"config_path", configPath,
		}
		if policy.Hint != "" {
			attrs = append(attrs, "hint", policy.Hint)
		}
		logger.Warn("codex sandbox: running unsandboxed with danger-full-access", attrs...)
	}

	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	return nil
}

// --- 迷你 semver 助手，限定在本包内以避免与 server/pkg/agent 形成导入环。
// agent 包已有类似解析器；这里复制最小片段，因为 execenv 不能依赖 agent。

type codexSemver struct {
	Major, Minor, Patch int
}

var codexSemverRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// parseCodexSemver 从形如 `v0.121.0` 的字符串解析出三段的 semver。
func parseCodexSemver(raw string) (codexSemver, error) {
	m := codexSemverRe.FindStringSubmatch(raw)
	if m == nil {
		return codexSemver{}, fmt.Errorf("cannot parse version %q", raw)
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return codexSemver{Major: maj, Minor: min, Patch: pat}, nil
}

// lessThan 按先 major、再 minor、最后 patch 的顺序比较两个版本。
func (v codexSemver) lessThan(o codexSemver) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}
