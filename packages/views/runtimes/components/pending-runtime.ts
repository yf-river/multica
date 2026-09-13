// ============================================================================
// pending-runtime.ts——为「已创建、但还没真正注册成行的自定义 runtime」造占位记录
//
// 角色定位：自定义运行配置（runtime profile）创建并绑到某台机器后，daemon 要过
// 一阵子才会真正把对应的 runtime 拉起来、上报一条 runtime 记录。为了 UI 上
// 「这条配置已经存在」能立刻可见（而不是等到 daemon 跑完才冒出来），本模块把
// profile 伪装成一条 AgentRuntime 占位记录，混进机器 / 未分配列表一起渲染。
//
// 识别方式：真实 runtime 由 daemon 上报，metadata 里没有 pending 标记；凡是
// metadata 中 pending_custom_runtime === true 的都是本模块伪造的占位行。
//
// 顶替规则：一旦 daemon 真正注册了该 profile 对应的 runtime（列表里出现同
// profile_id 的真实行），pending 占位就会被过滤掉（见 pendingRuntimesForProfiles）。
// 若超过 PENDING_RUNTIME_WARNING_MS 仍没被顶替，UI 依据 isPendingCustomRuntimeWarning
// 给出「可能没跑起来」的告警。
// ============================================================================
import type { AgentRuntime, RuntimeProfile } from "@multica/core/types";

// 占位行的告警阈值（45 秒）：挂起超过这个时长还没有真实 runtime 顶替，
// 就提示用户这条自定义 runtime 可能没注册成功。
export const PENDING_RUNTIME_WARNING_MS = 45_000;

// 占位行 id 的前缀。加前缀是为了和真实 runtime id 空间隔开、可辨认，
// 也保证同一个 profile 每次生成的占位 id 稳定（可被 React key 复用）。
const PENDING_RUNTIME_ID_PREFIX = "pending-runtime-profile:";

// 打在占位行 metadata 上的字段：全部用于 UI 判断「这是一条什么状态的占位行」。
// 真实 daemon 上报的 runtime 不会带这些键。
interface PendingRuntimeMetadata extends Record<string, unknown> {
  pending_custom_runtime: true;
  runtime_profile_id: string;
  runtime_profile_enabled: boolean;
  command_name: string;
  pending_since: string;
}

// 一条待注册 profile 的最小输入：profile 本体 + 它被创建的时间戳（毫秒）。
export interface PendingRuntimeProfile {
  profile: RuntimeProfile;
  createdAt: number;
}

// 生成某 profile 对应的占位 runtime id。
export function pendingRuntimeId(profileId: string): string {
  return `${PENDING_RUNTIME_ID_PREFIX}${profileId}`;
}

// 判断一条 runtime 是不是本模块伪造的「挂起占位」行（metadata 打标）。
export function isPendingCustomRuntime(runtime: AgentRuntime): boolean {
  return runtime.metadata?.pending_custom_runtime === true;
}

// 占位行里代表「被禁用的自定义 runtime」：挂起 + 对应 profile 已停用。
export function isDisabledCustomRuntime(runtime: AgentRuntime): boolean {
  return (
    isPendingCustomRuntime(runtime) &&
    runtime.metadata?.runtime_profile_enabled === false
  );
}

// 从占位行 metadata 里取出它包装的命令名（可空串时返回 null）。
export function pendingRuntimeCommandName(runtime: AgentRuntime): string | null {
  const command = runtime.metadata?.command_name;
  return typeof command === "string" && command.trim() ? command : null;
}

// 读出「自定义 runtime 注册失败」的原因：daemon 尝试注册失败时会在 runtime 的
// metadata 上写 runtime_profile_registration_error=true 与失败原因，这里把原因
// 取出来给 UI 展示；没有失败标记或原因空白则返回 null。
export function customRuntimeRegistrationFailure(
  runtime: AgentRuntime,
): string | null {
  if (runtime.metadata?.runtime_profile_registration_error !== true) return null;
  const reason = runtime.metadata.runtime_profile_failure_reason;
  return typeof reason === "string" && reason.trim() ? reason : null;
}

// 占位行是否已超过告警阈值：以 metadata.pending_since 为起点算时长，时间戳
// 非法（拿不到起算点）就不告警。
export function isPendingCustomRuntimeWarning(
  runtime: AgentRuntime,
  now: number,
): boolean {
  if (!isPendingCustomRuntime(runtime)) return false;
  const pendingSince = runtime.metadata?.pending_since;
  if (typeof pendingSince !== "string") return false;
  const startedAt = new Date(pendingSince).getTime();
  if (!Number.isFinite(startedAt)) return false;
  return now - startedAt >= PENDING_RUNTIME_WARNING_MS;
}

/**
 * 把一条 profile 物化成一条「挂起中」的 AgentRuntime 占位行。
 *
 * @param profile - 待注册的 profile。
 * @param createdAt - 创建时间戳（毫秒），用作占位行的 pending_since / 时间字段。
 * @param ownerId - 占位行的归属者；不传则回退到 profile.created_by。
 * @param localDaemonId - 已绑定的本机 daemon id（有机器时）；没有则填 null。
 * @param localMachineName - 本机机器名（有机器时），会拼进名字与 device_info；
 *   留空则回退 fallbackMachineName，再兜底 "Pending custom runtimes"。
 * @param fallbackMachineName - 没绑机器时用的兜底「机器」名。
 *
 * 伪造的占位行固定 status="offline"、runtime_mode="local"、visibility="private"，
 * metadata 打上 pending 标记——之后列表 / 健康推导都能把它当普通行处理。
 */
export function pendingRuntimeFromProfile({
  profile,
  createdAt,
  ownerId,
  localDaemonId,
  localMachineName,
  fallbackMachineName,
}: {
  profile: RuntimeProfile;
  createdAt: number;
  ownerId?: string | null;
  localDaemonId?: string | null;
  localMachineName?: string | null;
  fallbackMachineName?: string | null;
}): AgentRuntime {
  const pendingSince = new Date(createdAt).toISOString();
  const machineName =
    localMachineName?.trim() ||
    fallbackMachineName?.trim() ||
    "Pending custom runtimes";
  const metadata: PendingRuntimeMetadata = {
    pending_custom_runtime: true,
    runtime_profile_id: profile.id,
    runtime_profile_enabled: profile.enabled,
    command_name: profile.command_name,
    pending_since: pendingSince,
  };

  return {
    id: pendingRuntimeId(profile.id),
    workspace_id: profile.workspace_id,
    daemon_id: localDaemonId ?? null,
    name: `${profile.display_name} (${machineName})`,
    runtime_mode: "local",
    provider: profile.protocol_family,
    launch_header: profile.protocol_family,
    status: "offline",
    device_info: machineName,
    metadata,
    owner_id: ownerId ?? profile.created_by ?? null,
    visibility: "private",
    profile_id: profile.id,
    last_seen_at: pendingSince,
    created_at: pendingSince,
    updated_at: pendingSince,
  };
}

/**
 * 在真实 runtime 列表末尾追加「还没注册」的 profile 占位行。
 *
 * @param pendingProfiles - 候选 profile 列表（通常来自 runtimeProfileListOptions）。
 * @param runtimes - 当前机器已注册的真实 runtime 列表。
 * @returns 真实行 + 追加的占位行；若真实列表里已存在同 profile_id 的行，则说明
 *   该 profile 已经注册成功，不再为其造占位。空候选直接原样返回 runtimes。
 */
export function pendingRuntimesForProfiles({
  pendingProfiles,
  runtimes,
  ownerId,
  localDaemonId,
  localMachineName,
  fallbackMachineName,
}: {
  pendingProfiles: PendingRuntimeProfile[];
  runtimes: AgentRuntime[];
  ownerId?: string | null;
  localDaemonId?: string | null;
  localMachineName?: string | null;
  fallbackMachineName?: string | null;
}): AgentRuntime[] {
  if (pendingProfiles.length === 0) return runtimes;
  const registeredProfileIds = new Set(
    runtimes
      .map((runtime) => runtime.profile_id)
      .filter((profileId): profileId is string => !!profileId),
  );
  const pendingRuntimes = pendingProfiles
    .filter(({ profile }) => !registeredProfileIds.has(profile.id))
    .map(({ profile, createdAt }) =>
      pendingRuntimeFromProfile({
        profile,
        createdAt,
        ownerId,
        localDaemonId,
        localMachineName,
        fallbackMachineName,
      }),
    );
  return [...runtimes, ...pendingRuntimes];
}
