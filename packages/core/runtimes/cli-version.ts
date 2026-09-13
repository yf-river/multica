// 本文件集中做"快速创建 / 交接注记 / 聊天项目上下文 / worktree 能力"对 daemon 自带的
// multica CLI 版本的能力门槛判断。判断一律在"daemon 上报的版本串"这一双方共享的信号上
// 进行，前端与服务端据此天然一致；每个导出都注释了是硬门槛还是软门槛、以及为何这样定。
/**
 * 服务端 MinQuickCreateCLIVersion 门槛的前端镜像。快速创建 Agent（Quick Create
 * 弹窗）要求 daemon 自带的 multica CLI 至少是这个版本——旧版 daemon 要么在部分
 * CLI 失败时把议题建重复、要么丢掉快速创建的附件绑定，要么处理不好粘贴的截图 URL
 * （见 PR #1851 / MUL-1496）。
 *
 * 弹窗里的前端预检与服务端的 `/api/issues/quick-create` 处理器都会强制执行；服务端
 * 才是权威的信任边界，前端只是让用户可以在点提交之前就知道"你的 daemon 需要升级"。
 */
export const MIN_QUICK_CREATE_CLI_VERSION = "0.2.21";
export const MIN_QUICK_CREATE_FIELDS_CLI_VERSION = "0.4.3";

export type CliVersionState = "ok" | "too_old" | "missing";

export interface CliVersionCheck {
  state: CliVersionState;
  /** daemon 上报的版本串；缺失或解析不了时为空。 */
  current: string;
  /** 我们做门槛判断的硬性最低版本。 */
  min: string;
}

const SEMVER_RE = /v?(\d+)\.(\d+)\.(\d+)/;

// 匹配 `git describe --tags --always --dirty` 对"打过标签之后又提交过"的构建的输出，
// 例如 `v0.2.15-235-gdaf0e935` 或 `v0.2.15-235-gdaf0e935-dirty`。从源码构建的 daemon
// （Makefile `make build` / `make daemon`）会报这种形态；打标签的正式发布则是裸 semver。
// 把这种 dev 描述形态当成 OK，能放行源码自建的开发 daemon，又不会放宽对跑着过期稳定版
// 的预发/生产用户的限制。
const DEV_DESCRIBE_RE = /^v?\d+\.\d+\.\d+-\d+-g[0-9a-fA-F]+/;

function parseSemver(raw: string): [number, number, number] | null {
  const m = SEMVER_RE.exec(raw.trim());
  if (!m) return null;
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function lessThan(a: [number, number, number], b: [number, number, number]) {
  if (a[0] !== b[0]) return a[0] < b[0];
  if (a[1] !== b[1]) return a[1] < b[1];
  return a[2] < b[2];
}

/**
 * 拿 daemon 上报的 CLI 版本串去和最低版本比较。空/解析不了的输入返回 `"missing"`
 * （fail closed——与服务端同策略），可解析但低于门槛的返回 `"too_old"`。
 * dev 自建的 daemon（git-describe 形态）一律 OK——版本串本身是双方共享的信号，
 * 所以前端和服务端天然一致。
 */
export function checkQuickCreateCliVersion(detected: string | undefined | null): CliVersionCheck {
  return checkCliVersion(detected, MIN_QUICK_CREATE_CLI_VERSION);
}

/** 针对快速创建的显式优先级与截止日期字段的能力门槛。 */
export function checkQuickCreateFieldsCliVersion(
  detected: string | undefined | null,
): CliVersionCheck {
  return checkCliVersion(detected, MIN_QUICK_CREATE_FIELDS_CLI_VERSION);
}

function checkCliVersion(
  detected: string | undefined | null,
  minimum: string,
): CliVersionCheck {
  const current = (detected ?? "").trim();
  if (DEV_DESCRIBE_RE.test(current)) {
    return { state: "ok", current, min: minimum };
  }
  const parsed = current ? parseSemver(current) : null;
  if (!parsed) {
    return { state: "missing", current, min: minimum };
  }
  const min = parseSemver(minimum)!;
  if (lessThan(parsed, min)) {
    return { state: "too_old", current, min: minimum };
  }
  return { state: "ok", current, min: minimum };
}

/** 从 runtime 行那个弱类型的 metadata 包里取出 `cli_version`。 */
export function readRuntimeCliVersion(metadata: Record<string, unknown> | undefined): string {
  const v = metadata?.cli_version;
  return typeof v === "string" ? v : "";
}

/**
 * 服务端 `MinHandoffCLIVersion` 软门槛（`server/pkg/agent/version.go`）的前端镜像。
 * 指派交接注记（handoff note）只会被"multica CLI 版本不低于此值"的 daemon 写进
 * 运行的开场提示里（MUL-3375）；旧 daemon 会悄悄丢掉它。与快速创建门槛不同，
 * 这里从不拦截指派——UI 只是把注记输入框置灰并给出警告。
 *
 * 要与服务端常量保持同步；两边独立强制执行（服务端权威），但必须一致，
 * 警告才会和现实吻合。
 */
export const MIN_HANDOFF_CLI_VERSION = "0.3.28";

/**
 * daemon 上报的 CLI 版本是否够新、能渲染交接注记。镜像服务端的 `agent.HandoffSupported`：
 * 缺失 / 解析不了 / 低于最低版本都降级为 `false`；dev 自建 daemon（git-describe 形态）
 * 一律放行——版本串是共享信号，前端与服务端天然一致。纯同步函数，注记框可以直接
 * 依据已热的 runtime 缓存落定，不必等 trigger-preview 那趟往返——与快速创建版本门槛一致。
 */
export function handoffSupported(detected: string | undefined | null): boolean {
  return meetsMinCliVersion(detected, MIN_HANDOFF_CLI_VERSION);
}

/**
 * 首个会把聊天会话的项目上下文（描述 + 资源）渲染进运行简报的版本
 * （PR #5765，随 v0.4.10 发布）。更旧的 daemon 仍会收到并遵循项目的 repos——服务端
 * 先把它们预提取进通用的 `repos` claim 字段——但会悄悄跳过 Project Context 段，
 * 于是那段持久描述永远到不了 Agent 手里。软门槛：选项目总能成功；UI 只提示。
 *
 * 纯前端常量：和 handoff 不同，这里没有服务端预览接口在算它，所以也没有
 * 需要保持同步的服务端孪生常量。
 */
export const MIN_CHAT_PROJECT_CONTEXT_CLI_VERSION = "0.4.10";

/**
 * daemon 上报的 CLI 版本是否够新、能把聊天会话的项目描述注入运行简报。
 * 降级规则与 `handoffSupported` 相同：缺失 / 解析不了 / 低于最低版本都为 `false`，
 * dev 自建 daemon（git-describe 形态）一律放行。
 */
export function chatProjectContextSupported(detected: string | undefined | null): boolean {
  return meetsMinCliVersion(detected, MIN_CHAT_PROJECT_CONTEXT_CLI_VERSION);
}

function meetsMinCliVersion(detected: string | undefined | null, minimum: string): boolean {
  const current = (detected ?? "").trim();
  if (!current) return false;
  if (DEV_DESCRIBE_RE.test(current)) return true;
  const parsed = parseSemver(current);
  if (!parsed) return false;
  return !lessThan(parsed, parseSemver(minimum)!);
}

/**
 * daemon 在实现了 local_directory 资源的 worktree 模式时对外宣告的能力标识。
 * 镜像 `server/pkg/protocol/messages.go` 里的 `DaemonCapabilityLocalWorktreeV1`。
 */
export const LOCAL_WORKTREE_CAPABILITY = "local-worktree-v1";

/** 本模块所需的最小 runtime 形态；避免调用方被迫去引大类型。 */
type RuntimeCapabilityRow = {
  daemon_id?: string | null;
  last_seen_at?: string | null;
  metadata?: unknown;
};

/**
 * `daemonId` 背后的那台机器是否"宣告过"支持 worktree，按其最近一次可见的
 * runtime 行判断。
 *
 * 刻意只做正向信号、刻意不做门槛。`false` 表示"没有任何 runtime 行说支持"——
 * 这既可能是真不支持，也可能只是行写于服务端开始记录能力之前，或服务端太老、
 * 压根没记录过。三者补救方式不同，而客户端无法可靠区分：它得从后端写下的数据里
 * 反推后端的年代，而且一行的沉默会一直活到修复它的那次升级之后（#7113）。
 *
 * 所以客户端放弃了猜测，改为在每个写路径（创建项目、加资源、改资源）以及认领时
 * 去问服务端——服务端天生知道自己的版本——认领那一步才是真正把 Agent 挡在用户
 * 工作副本之外的闸门（MUL-5707）。本函数只决定是否**预选**并行模式：猜错的代价
 * 是一个单选按钮选错，而不是挡住用户或给出误导指令。
 *
 * 取最新行而非任意行：注销一台 runtime 只是把它标成离线，元数据仍保留，
 * 否则一台曾宣告过该能力、后来又降级的机器会永远替自己背书。
 */
export function runtimeAdvertisesLocalWorktree(
  runtimes: RuntimeCapabilityRow[],
  daemonId: string | null | undefined,
): boolean {
  if (!daemonId) return false;
  let newest: RuntimeCapabilityRow | undefined;
  for (const rt of runtimes) {
    if (rt.daemon_id !== daemonId) continue;
    if (!newest) {
      newest = rt;
      continue;
    }
    // 从未上报过时间的行排最旧，所以活着的行总会胜出。
    const candidateSeen = rt.last_seen_at ?? "";
    const currentSeen = newest.last_seen_at ?? "";
    if (candidateSeen > currentSeen) newest = rt;
  }
  const metadata = newest?.metadata;
  if (!metadata || typeof metadata !== "object") return false;
  const caps = (metadata as { capabilities?: unknown }).capabilities;
  return Array.isArray(caps) && caps.includes(LOCAL_WORKTREE_CAPABILITY);
}
