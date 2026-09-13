package agent

// 文件职责：agent CLI / multica CLI 的版本门槛校验。
//
// 两条校验线：一条是针对各 agent 运行时的 MinVersions（低于门槛的 agent CLI 在
// daemon 注册阶段即被拒）；另一条是 multica CLI 自己的能力门槛（MinQuickCreateCLIVersion
// 等，硬/软闸见各常量说明）。版本解析统一走 parseSemver（宽容的正则，能从中抽出
// major.minor.patch 就行），git-describe 形态的 dev 构建有专门的放行正则，保证
// `make daemon` 不被自己挡住。

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinVersions 定义每种 agent 类型要求的最低 CLI 版本。低于这些的版本会在 daemon
// 注册阶段被拒。
var MinVersions = map[string]string{
	"claude":   "2.0.0",
	"codex":    "0.100.0", // 0.100.0 起才有 app-server --listen stdio://
	"copilot":  "1.0.0",   // --output-format json 的外壳从 1.0.x 起稳定
	"grok":     "0.2.89",  // ACP + authenticate/session-load/set_model/MCP 与 --effort 思考旗标
	"qwen":     "0.20.0",  // stream-json 协议是按 Qwen Code 0.20.0 抓包并核实的
	"dim":      "0.3.10",  // 跨运行的 session/load：按进程加锁、优雅退出时释放
	"mcode":    "0.1.2",   // ACP v1 session/new、prompt、MCP 能力转发
	"zeroclaw": "0.8.0",   // 0.8.0 起才有持久化 ACP 会话与 session/resume
}

// MinQuickCreateCLIVersion 用 daemon 注册时上报的 multica CLI 版本给
// agent-create（quick-create）流程设门槛。agent 要跑的 quick-create prompt 依赖
// 该版本之后引入的 CLI 行为（附件 URL 处理、quick-create 附件绑定、`multica issue
// create` 失败时不再重试——见 PR #1851）；更老的 daemon 要么会重复建 issue，要么
// 处理错粘贴进来的截图 URL。按硬性要求对待：缺失/解析不了/低于门槛一律失败关闭。
const MinQuickCreateCLIVersion = "0.2.21"

// MinQuickCreateFieldsCLIVersion 是首个能把 claim 响应里的显式 quick-create 优先级
// 与截止字段带进生成的 issue-create prompt 的 daemon 版本。基础 quick-create 仍用
// 上面那个更低的门槛；只有用到这些可选字段的请求才需要本门槛。
const MinQuickCreateFieldsCLIVersion = "0.4.3"

// MinLocalWorktreeCLIVersion 是首个为 local_directory 资源交付
// execution_mode=worktree 的版本（MUL-5707）。
//
// 没有任何东西以它为闸。它只是个展示值：422 载荷和 UI 提示里显示的数，让用户大致
// 知道要升到哪个版本。真正的闸读的是 protocol.DaemonCapabilityLocalWorktreeV1——
// daemon 只在真正实现该模式时才宣告它。
//
// 它不再是闸，因为它不可能当闸。没有该实现的 daemon 不会丢字段——它会在原地跑任务，
// 直接编辑用户要求隔离的工作副本。版本字符串回答不了这个：CheckMinCLIVersionFor 对
// git-describe 的 dev 构建放行让 `make daemon` 不被卡，于是某个 v0.4.23 时代的
// daemon 报着 "v0.4.21-24-gcd3c0bb89" 就穿过门槛，在用户自己的目录里跑起了两个任务。
const MinLocalWorktreeCLIVersion = "0.4.24"

// MinHandoffCLIVersion 是最低 multica CLI 版本——从它起 daemon 会把交接笔记渲染进
// 运行的开场 prompt 与 issue_context.md（MUL-3375）。与 quick-create 不同这是软闸：
// 带笔记分派 issue 在旧 daemon 上从不失败——分派照样生效，只是笔记被悄悄丢掉。
// 前端读 HandoffSupported 来置灰笔记框并警告用户，免得被一个静默忽略的笔记打
// 个措手不及。升版时把这里改成真正交付该 daemon 渲染的版本。
const MinHandoffCLIVersion = "0.3.28"

// HandoffSupported 报告上报 cliVersion 的 daemon 是否新到能渲染交接笔记。复用
// CheckMinCLIVersion 的解析（含 git-describe dev 构建豁免），但绝不返回错误——
// 缺失/过老/解析不了都只是"不支持"，软闸可以优雅降级。
func HandoffSupported(cliVersion string) bool {
	d := strings.TrimSpace(cliVersion)
	if d == "" {
		return false
	}
	if devDescribeRe.MatchString(d) {
		return true
	}
	parsed, err := parseSemver(d)
	if err != nil {
		return false
	}
	min, err := parseSemver(MinHandoffCLIVersion)
	if err != nil {
		return false
	}
	return !parsed.lessThan(min)
}

// CheckMinCLIVersion 返回的哨兵错误。调用方据此分支，用合适的用户文案区分
// "需要升级"与"版本未上报"。
var (
	ErrCLIVersionMissing = errors.New("multica CLI version not reported by daemon")
	ErrCLIVersionTooOld  = errors.New("multica CLI version is below required minimum")
)

// devDescribeRe 匹配"构建超出最近 tag"时 `git describe --tags --always --dirty`
// 的输出，例如 `v0.2.15-235-gdaf0e935`（可选带 `-dirty` 后缀）。从源码构建的
// daemon（Makefile `make build` / `make daemon`）上报这种形态；tag 发布版是裸
// semver。放行 dev 形态的 daemon，既让 `make daemon` 不被卡，又不削弱对 staging /
// 生产里跑过期稳定版的用户的门槛。
var devDescribeRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+-\d+-g[0-9a-fA-F]+`)

// CheckMinCLIVersion 在 `detected` 解析为 ≥ minimum 时返回 nil。空输入或解析不了
// 返回 ErrCLIVersionMissing；能解析但低于最低值返回 ErrCLIVersionTooOld。调用方可
// 用 errors.Is 匹配这两个哨兵错误来定响应形态。
//
// dev 构建的 daemon（git-describe 形态）恒通过——版本字符串本身就是共享信号，
// 所以模态预检与这个 server 闸天然一致，无需比对各自独立的环境变量。
func CheckMinCLIVersion(detected string) error {
	return CheckMinCLIVersionFor(detected, MinQuickCreateCLIVersion)
}

// CheckMinCLIVersionFor 用调用方提供的能力下限应用 quick-create 版本策略。它保留
// dev 构建豁免，让特性专属的 server 与前端闸与基础闸保持一致。
func CheckMinCLIVersionFor(detected, minimum string) error {
	d := strings.TrimSpace(detected)
	if d == "" {
		return ErrCLIVersionMissing
	}
	if devDescribeRe.MatchString(d) {
		return nil
	}
	parsed, err := parseSemver(d)
	if err != nil {
		return ErrCLIVersionMissing
	}
	min, err := parseSemver(minimum)
	if err != nil {
		// 常量本身写错了——按"缺失"失败关闭。
		return ErrCLIVersionMissing
	}
	if parsed.lessThan(min) {
		return ErrCLIVersionTooOld
	}
	return nil
}

// semver 持有一个已解析的语义化版本（major.minor.patch）。
type semver struct {
	Major, Minor, Patch int
}

// versionRe 匹配 "2.1.100"、"v2.0.0" 或 "2.1.100 (Claude Code)" 这类版本串——
// 抽出前三个数字分量。
var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// parseSemver 从版本字符串里抽出语义化版本。
func parseSemver(raw string) (semver, error) {
	m := versionRe.FindStringSubmatch(raw)
	if m == nil {
		return semver{}, fmt.Errorf("cannot parse version %q", raw)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return semver{Major: major, Minor: minor, Patch: patch}, nil
}

// lessThan 当 v < other 时返回 true。
func (v semver) lessThan(other semver) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}

// BelowMinimumError 报告一个"解析成功、且低于配置下限"的版本。独立类型让调用方能
// 把"已确证过老"的判决与"版本解析不了"区分开：只有前者才是强到足以行动的
// （把运行时判下线），不可读的版本必须与任何其它检测失败同等对待——别动正在正常
// 工作的运行时。
type BelowMinimumError struct {
	AgentType string
	Detected  string
	Minimum   string
}

func (e *BelowMinimumError) Error() string {
	return fmt.Sprintf("%s version %s is below minimum required %s — please upgrade", e.AgentType, e.Detected, e.Minimum)
}

// CheckMinVersion 校验 detectedVersion 是否达到 agentType 的最低要求。版本可接受
// 或未定义下限时返回 nil；版本解析成功且确认过老时返回 *BelowMinimumError；
// 版本完全解析不了时返回普通错误。
func CheckMinVersion(agentType, detectedVersion string) error {
	minRaw, ok := MinVersions[agentType]
	if !ok {
		return nil
	}
	min, err := parseSemver(minRaw)
	if err != nil {
		return fmt.Errorf("invalid minimum version %q for %s: %w", minRaw, agentType, err)
	}
	detected, err := parseSemver(detectedVersion)
	if err != nil {
		return fmt.Errorf("cannot parse detected %s version %q: %w", agentType, detectedVersion, err)
	}
	if detected.lessThan(min) {
		return &BelowMinimumError{AgentType: agentType, Detected: detectedVersion, Minimum: minRaw}
	}
	return nil
}
