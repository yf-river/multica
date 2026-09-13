// ============================================================================
// 机器行里的 CLI 版本/升级区块（MachineCliSection）
//
// 角色定位：机器列表/详情里每一台机器展示自己 CLI 状态的小区块。本机
// （local）机器交给 UpdateSection 做「版本 + 升级」；云机器只是单行的静态
// 版本号。要点在于把「谁能执行升级命令」的判断与「所有人都能看的版本信息」
// 分开——前者受权限约束（见 machineUpdateRuntime），后者不受。
//
// 数据流：父组件传入 RuntimeMachine 与当前用户信息；本组件先挑出一个可用的
// 命令通道 runtime，再决定渲染升级控件还是静态版本。没有任何可展示信息时
// 返回 null（不占版面）。
// ============================================================================
import type { AgentRuntime } from "@multica/core/types";
import type { RuntimeMachine } from "./runtime-machines";
import { UpdateSection } from "./update-section";

/**
 * 为一次机器级的 daemon 升级，挑一台观看者能拿来当「命令通道」的 runtime。
 *
 * 观看者可以用自己名下的 runtime；若他是工作区 owner/admin，也可以动用公开
 * runtime。有多台可选时优先挑在线的——daemon 只有在线的通道才收得到升级
 * 指令。非本机（云）机器没有本地 daemon 可升级，直接返回 null。
 */
export function machineUpdateRuntime(
  machine: RuntimeMachine,
  currentUserId: string | undefined,
  canManagePublicRuntimes: boolean,
): AgentRuntime | null {
  if (machine.mode !== "local") return null;

  const usable = currentUserId
    ? machine.runtimes.filter(
        (runtime) =>
          runtime.owner_id === currentUserId ||
          (canManagePublicRuntimes && runtime.visibility === "public"),
      )
    : [];
  return (
    usable.find((runtime) => runtime.status === "online") ??
    usable[0] ??
    null
  );
}

// 机器级 CLI 区块：本机机器渲染升级控件（UpdateSection），云机器只渲染静态
// 版本号。本机模式但既无可用命令通道、又无版本号可展示时返回 null——什么
// 都不值得占这块版面。
export function MachineCliSection({
  machine,
  currentUserId,
  canManagePublicRuntimes = false,
}: {
  machine: RuntimeMachine;
  currentUserId: string | undefined;
  canManagePublicRuntimes?: boolean;
}) {
  const updateRuntime = machineUpdateRuntime(
    machine,
    currentUserId,
    canManagePublicRuntimes,
  );

  if (machine.mode !== "local") {
    return machine.cliVersion ? (
      <span className="font-mono">CLI {machine.cliVersion}</span>
    ) : null;
  }

  // 观看者有没有权限发升级命令，绝不能反过来卡住「机器级版本信息」的展示：
  // 版本号是人人都能看的机器事实，不随权限缩水。
  if (
    !updateRuntime &&
    machine.runtimes.length === 0 &&
    !machine.cliVersion
  ) {
    return null;
  }

  return (
    <UpdateSection
      runtimeId={updateRuntime?.id ?? null}
      currentVersion={machine.cliVersion}
      isOnline={updateRuntime?.status === "online"}
    />
  );
}
