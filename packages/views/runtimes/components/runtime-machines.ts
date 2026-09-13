// ============================================================================
// runtime-machines.ts——把一条条原始 runtime 记录聚合成「机器」视图的纯逻辑模块
//
// 角色定位：runtimes 列表页 / 详情页需要先按「物理 / 托管单元」把 runtime 归并成
// 机器行（RuntimeMachine）再渲染。本模块只做聚合、筛选、排序与字段推导，不碰 UI、
// 不发请求，是页面数据层的纯函数部分。
//
// 背景知识：一台机器（如装了 multica daemon 的电脑、或云端托管节点）上往往同时
// 跑着多个 agent runtime——每个厂商的 CLI 各注册一行。所以「机器 = 一组 runtime」，
// 机器的标题 / 健康 / 版本等属性都要从这组 runtime 里推导出来。
//
// 分组规则（runtimeMachineId）：优先按 daemon（daemon_id 相同即同一台机器）；
// 没有 daemon 的记录退化为按「设备名」，还不行才让这条 runtime 自己单独成一台。
//
// 关键概念：
//  - 机器标题（machineTitle）：用户显式改的名（机器级改名会把同一 custom_name 盖到
//    这台机器全部 runtime 上，见 sharedCustomName / MUL-4217）最优先，其次是 OS
//    上报的设备名，再其次是「厂商 cloud」，最后兜底 daemon id 缩写。
//  - 健康（health）：机器下所有 runtime 取「最差」档，但只要有一个 online 就整台算
//    online——机器还在跑就有资格被当作在线。
//  - 排序（compareRuntimeMachines）：remote（本机/远程 daemon）排在 cloud 前；
//    同档内在线数多的在前，再按标题字母序。
// ============================================================================
import { deriveRuntimeHealth, type RuntimeHealth } from "@multica/core/runtimes";
import type { AgentRuntime } from "@multica/core/types";
import { formatDeviceInfo } from "../utils";

// 机器的两种来源：remote = 装了 daemon 的远端/本机机器，cloud = multica 云端托管节点。
export type RuntimeMachineSection = "remote" | "cloud";
// 列表过滤维度：全部 / 至少一个在线 / 有问题的机器。
export type RuntimeMachineFilter = "all" | "online" | "issues";

// 一台机器当前承载的工作负载摘要（汇总自它名下各 runtime）。
export interface RuntimeWorkloadSummary {
  runningCount: number;
  queuedCount: number;
}

// 聚合后的「机器」行：由同名 daemon（或设备）下的若干 runtime 汇总而来，
// 是列表页与详情页的渲染单位。字段要么来自 daemon 共享属性，要么由整组
// runtime 推导（健康取最差、标题按优先级猜、计数是求和）。
export interface RuntimeMachine {
  id: string;
  daemonId: string | null;
  title: string;
  subtitle: string | null;
  deviceInfo: string | null;
  cliVersion: string | null;
  mode: AgentRuntime["runtime_mode"];
  section: RuntimeMachineSection;
  health: RuntimeHealth;
  runtimes: AgentRuntime[];
  onlineCount: number;
  issueCount: number;
  runningCount: number;
  queuedCount: number;
  providerNames: string[];
  lastSeenAt: string | null;
}

interface RuntimeMachineOptions {
  now: number;
  workloadByRuntimeId?: Map<string, RuntimeWorkloadSummary>;
}

// 聚合过程中的中间态：先按 key 攒 runtime，攒齐后再一次推导全部展示字段。
interface RuntimeMachineDraft {
  id: string;
  daemonId: string | null;
  mode: AgentRuntime["runtime_mode"];
  runtimes: AgentRuntime[];
}

// 健康档次的严重程度数字，用于把整组 runtime 里「最严重」的那个挑出来当机器健康。
const HEALTH_SEVERITY: Record<RuntimeHealth, number> = {
  online: 0,
  recently_lost: 1,
  offline: 2,
  long_offline: 3,
};

/**
 * 把「名字（主机名）」形态的 runtime 名字拆成基础名与括号里的主机名。
 *
 * daemon 上报的 runtime 名形如 "Claude Code (alice-mac.local)"——括号内是
 * 这台机器的 OS 主机名，是判断「哪些 runtime 属于同一台机器」的证据之一。
 * 没有括号就原样返回，hostname 给 null。
 */
export function splitRuntimeName(name: string): {
  base: string;
  hostname: string | null;
} {
  const m = name.match(/^(.+?)\s+\(([^)]+)\)$/);
  if (!m || !m[1] || !m[2]) return { base: name, hostname: null };
  return { base: m[1], hostname: m[2] };
}

// 显示在机器名下方/旁边的单条 runtime 标签。机器级改名会把同一个 custom_name
// 盖到这台 daemon 的每个 runtime 上（MUL-4217），此时逐行重复同一个名字只是噪音
// ——回退到协议族基础名（如 "Claude"）。只有某条 runtime 被单独改名、与机器名
// 不同时，才原样展示这个自定义名。
export function runtimeRowLabel(
  runtime: AgentRuntime,
  machineTitle: string,
): string {
  const custom = runtime.custom_name?.trim();
  if (custom && custom !== machineTitle) return custom;
  return splitRuntimeName(runtime.name).base;
}

/**
 * 把扁平的一串 runtime 记录按所属机器聚合成 RuntimeMachine 列表。
 *
 * @param runtimes - 后端返回的全部 runtime（来自 runtimeListOptions）。
 * @param options - now 用于推导健康（时间阈值判断）；workloadByRuntimeId 是
 *   「runtime id → 正在跑/排队数」的索引，用于汇总每台机器的负载。
 * @returns 聚合好的机器列表，已按 compareRuntimeMachines 排好序。
 *
 * 内部流程：先按 runtimeMachineId 分组攒 draft，再逐个 finalizeRuntimeMachine
 * 推导标题/健康/负载等字段。
 */
export function buildRuntimeMachines(
  runtimes: AgentRuntime[],
  options: RuntimeMachineOptions,
): RuntimeMachine[] {
  const drafts = new Map<string, RuntimeMachineDraft>();

  for (const runtime of runtimes) {
    const id = runtimeMachineId(runtime);
    const draft =
      drafts.get(id) ??
      ({
        id,
        daemonId: runtime.daemon_id,
        mode: runtime.runtime_mode,
        runtimes: [],
      } satisfies RuntimeMachineDraft);
    draft.runtimes.push(runtime);
    drafts.set(id, draft);
  }

  const machines = Array.from(drafts.values()).map((draft) =>
    finalizeRuntimeMachine(draft, options),
  );

  return machines.sort(compareRuntimeMachines);
}

/**
 * 按「状态页签 + 关键字」过滤机器列表。
 *
 * @param machines - buildRuntimeMachines 的结果。
 * @param query - 关键字，大小写不敏感；命中标题 / 副标题 / 设备信息 /
 *   daemon id / 厂商名 / runtime 名与自定义名任一即可。
 * @param filter - "online" 只要至少一个 runtime 在线的机器；
 *   "issues" 只要有问题（非全部在线）的机器。
 */
export function filterRuntimeMachines(
  machines: RuntimeMachine[],
  query: string,
  filter: RuntimeMachineFilter,
): RuntimeMachine[] {
  const q = query.trim().toLowerCase();
  return machines.filter((machine) => {
    if (filter === "online" && machine.onlineCount === 0) return false;
    if (filter === "issues" && machine.issueCount === 0) return false;
    if (!q) return true;

    const haystack = [
      machine.title,
      machine.subtitle,
      machine.deviceInfo,
      machine.daemonId,
      machine.providerNames.join(" "),
      machine.runtimes.map((runtime) => runtime.name).join(" "),
      machine.runtimes.map((runtime) => runtime.custom_name ?? "").join(" "),
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();

    return haystack.includes(q);
  });
}

/**
 * 汇总机器列表各页签的计数（全部 / 至少一个在线 / 有问题），喂给顶部的
 * Tab 徽标。注意 "online" 计的是「机器数」而非 runtime 数，与
 * filterRuntimeMachines 的判据一致。
 */
export function runtimeMachineCounts(machines: RuntimeMachine[]): {
  all: number;
  online: number;
  issues: number;
} {
  return {
    all: machines.length,
    online: machines.filter((machine) => machine.onlineCount > 0).length,
    issues: machines.filter((machine) => machine.issueCount > 0).length,
  };
}

// 把一个分组 draft 加工成完整的 RuntimeMachine：统一给组内 runtime 排序、挑标题、
// 推导副标题 / 健康 / 负载汇总，并选出有代表性的版本号与最后在线时间。
function finalizeRuntimeMachine(
  draft: RuntimeMachineDraft,
  options: RuntimeMachineOptions,
): RuntimeMachine {
  const runtimes = draft.runtimes.toSorted((a, b) =>
    a.provider.localeCompare(b.provider),
  );
  const first = runtimes[0];
  const providerNames = Array.from(new Set(runtimes.map((r) => r.provider))).sort();
  const title = machineTitle(runtimes);
  const deviceInfo = first ? formatDeviceInfo(first.device_info ?? null) : null;
  const subtitle = machineSubtitle({
    title,
    deviceInfo,
    daemonId: draft.daemonId,
    mode: draft.mode,
  });
  const healthByRuntime = runtimes.map((runtime) =>
    deriveRuntimeHealth(runtime, options.now),
  );
  const onlineCount = healthByRuntime.filter((h) => h === "online").length;
  const issueCount = runtimes.length - onlineCount;
  const health =
    onlineCount > 0
      ? "online"
      : healthByRuntime.reduce<RuntimeHealth>(
          (worst, current) =>
            HEALTH_SEVERITY[current] > HEALTH_SEVERITY[worst] ? current : worst,
          "recently_lost",
        );
  const workload = runtimes.reduce(
    (sum, runtime) => {
      const entry = options.workloadByRuntimeId?.get(runtime.id);
      return {
        runningCount: sum.runningCount + (entry?.runningCount ?? 0),
        queuedCount: sum.queuedCount + (entry?.queuedCount ?? 0),
      };
    },
    { runningCount: 0, queuedCount: 0 },
  );

  return {
    id: draft.id,
    daemonId: draft.daemonId,
    title,
    subtitle,
    deviceInfo,
    cliVersion: currentMachineMetadata(runtimes, "cli_version"),
    mode: draft.mode,
    section: draft.mode === "cloud" ? "cloud" : "remote",
    health,
    runtimes,
    onlineCount,
    issueCount,
    runningCount: workload.runningCount,
    queuedCount: workload.queuedCount,
    providerNames,
    lastSeenAt: latestLastSeenAt(runtimes),
  };
}

// 机器分组 key，按可靠度递减：同一 daemon_id 必是同一台机器（前缀 runtime_mode
// 是为了让本地 / 云的同 id 不撞）；没有 daemon 的旧记录退化用设备名归类；两者都
// 拿不到就只好让每条 runtime 单独成一台机器。
function runtimeMachineId(runtime: AgentRuntime): string {
  if (runtime.daemon_id) return `${runtime.runtime_mode}:${runtime.daemon_id}`;
  const deviceName = runtimeDeviceName(runtime);
  if (deviceName) return `${runtime.runtime_mode}:device:${deviceName}`;
  return `${runtime.runtime_mode}:runtime:${runtime.id}`;
}

// 从 runtime 记录里捞「设备名」：优先取名字括号里的主机名（形如
// "Claude Code (my-mac)" → "my-mac"）；没有括号就取 device_info 里以 " · "
// 分隔的首段当设备名兜底。
function runtimeDeviceName(runtime: AgentRuntime): string | null {
  const host = splitRuntimeName(runtime.name).hostname;
  if (host) return host;

  const raw = runtime.device_info?.trim();
  if (!raw) return null;
  return raw.split(" · ")[0]?.trim() || null;
}

/**
 * 返回这台机器上「所有 runtime 共享的自定义名」，没有就返回 null。
 *
 * 背景（MUL-4217）：机器级改名会把同一个 custom_name 写到这台 daemon 的全部
 * runtime 上，所以「所有 runtime 同名」的那个名字就是机器的标签；而只给单条
 * runtime 改的名（与他人不同）会被刻意忽略，避免它冒充整台机器的名字。
 */
export function sharedCustomName(runtimes: AgentRuntime[]): string | null {
  if (runtimes.length === 0) return null;
  const names = runtimes.map((r) => r.custom_name?.trim() ?? "");
  const first = names[0];
  if (!first) return null;
  return names.every((n) => n === first) ? first : null;
}

function machineTitle(runtimes: AgentRuntime[]): string {
  // 用户显式设过的机器名优先于一切，包括 OS 上报的本机名。
  const shared = sharedCustomName(runtimes);
  if (shared) return shared;

  const first = runtimes[0];
  if (!first) return "Unknown machine";

  const deviceName = runtimeDeviceName(first);
  if (deviceName) return deviceName;

  if (first.runtime_mode === "cloud") {
    return `${capitalize(first.provider)} cloud`;
  }
  return first.daemon_id ? shortDaemonId(first.daemon_id) : "Unknown machine";
}

function machineSubtitle({
  title,
  deviceInfo,
  daemonId,
  mode,
}: {
  title: string;
  deviceInfo: string | null;
  daemonId: string | null;
  mode: AgentRuntime["runtime_mode"];
}): string | null {
  const compact = compactDeviceInfo(deviceInfo, title);
  if (compact) return compact;
  if (daemonId) return `daemon ${shortDaemonId(daemonId)}`;
  return mode === "cloud" ? "Cloud worker" : null;
}

function compactDeviceInfo(
  deviceInfo: string | null,
  title: string,
): string | null {
  if (!deviceInfo) return null;
  const parts = deviceInfo
    .split(" · ")
    .map((part) => part.trim())
    .filter(Boolean)
    .filter((part) => part !== title)
    .filter((part) => !isAgentVersionLike(part));
  const primary = parts[0];
  if (!primary) return null;

  // 把 formatDeviceInfo 产出的「OS (arch)」（如 "macOS (x86_64)"）重排成更好扫读的
  // 「arch OS」——"x86_64 macOS"。带括号的另一形态是版本号，已在上面的过滤里被
  // 剔掉，不会混进每台机器的副标题。
  const osArch = primary.match(/^(.+?)\s+\(([^)]+)\)$/);
  if (osArch?.[1] && osArch[2]) {
    return `${osArch[2]} ${osArch[1]}`;
  }
  return primary;
}

// 命中「某 agent CLI 版本号」而不是机器信息的分段（如 "2.1.5 (Claude Code)"、
// "codex-cli 0.118.0"、"1.0.20"、"claude 1.0.0"）。这类内容是描述 runtime 的，
// 与宿主机无关，绝不该成为机器的副标题——否则每台装了 claude 的 daemon 行都
// 显示成 "Claude Code …"，真正的机器差异反而被淹没。
function isAgentVersionLike(part: string): boolean {
  return /(?:^|\s)v?\d+\.\d+\.\d+/.test(part);
}

function latestLastSeenAt(runtimes: AgentRuntime[]): string | null {
  let latest: string | null = null;
  for (const runtime of runtimes) {
    if (!runtime.last_seen_at) continue;
    if (!latest || new Date(runtime.last_seen_at) > new Date(latest)) {
      latest = runtime.last_seen_at;
    }
  }
  return latest;
}

function currentMachineMetadata(
  runtimes: AgentRuntime[],
  key: "cli_version",
): string | null {
  const online = runtimes.filter((runtime) => runtime.status === "online");
  const candidates = online.length > 0 ? online : runtimes;

  for (const runtime of candidates.toSorted(compareRuntimeReports)) {
    const value = runtime.metadata?.[key];
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return null;
}

function compareRuntimeReports(a: AgentRuntime, b: AgentRuntime): number {
  return runtimeReportTime(b) - runtimeReportTime(a);
}

function runtimeReportTime(runtime: AgentRuntime): number {
  const reportedAt = runtime.last_seen_at ?? runtime.updated_at;
  const timestamp = Date.parse(reportedAt);
  return Number.isNaN(timestamp) ? 0 : timestamp;
}

function shortDaemonId(daemonId: string): string {
  return daemonId.length > 12 ? `${daemonId.slice(0, 8)}...` : daemonId;
}

function capitalize(value: string): string {
  if (!value) return "Runtime";
  return `${value.slice(0, 1).toUpperCase()}${value.slice(1)}`;
}

function compareRuntimeMachines(a: RuntimeMachine, b: RuntimeMachine): number {
  const sectionDelta = sectionRank(a.section) - sectionRank(b.section);
  if (sectionDelta !== 0) return sectionDelta;
  if (a.onlineCount !== b.onlineCount) return b.onlineCount - a.onlineCount;
  return a.title.localeCompare(b.title);
}

function sectionRank(section: RuntimeMachineSection): number {
  switch (section) {
    case "remote":
      return 0;
    case "cloud":
      return 1;
  }
}
