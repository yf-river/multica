// Agent 创建流程的"草稿"模型：构建/校验/提交。所有创建入口（手动、复制、AI 构建器）
// 共享同一套字段与转换，保证无论从哪个入口进来，处理方式都一致。
import { isRuntimeUsableForUser } from "../runtimes/access";
import type {
  Agent,
  AgentInvocationTargetInput,
  AgentPermissionScope,
  AgentConversationStarter,
  CreateAgentRequest,
  RuntimeDevice,
} from "../types";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  AGENT_MAX_CONCURRENT_TASKS_MAX,
  AGENT_MAX_CONCURRENT_TASKS_MIN,
} from "./constants";

// 与其他 Agent 线上类型放一起声明，让 `StoredAgentDraft` 能引用它，而 types/
// 不必反向 import agents/ 的内容。
export type { AgentPermissionScope };

/**
 * 正在被创建的 Agent 的"进行中配置"。所有创建界面（手动、复制、AI 构建器）共用，
 * 使同一组字段无论从哪个入口进来都以同样的方式被校验、预填与提交。
 */
export interface AgentDraft {
  name: string;
  description: string;
  instructions: string;
  conversationStarters: AgentConversationStarter[];
  avatarUrl: string | null;
  runtimeId: string;
  model: string;
  /** 运行时原生的推理档位参数（thinking level），限定于当前 `model`。 */
  thinkingLevel: string;
  /** 运行时原生的执行档位（如 Codex Speed 的服务等级），限定于当前 `model`。 */
  serviceTier: string;
  skillIds: Set<string>;
  permissionScope: AgentPermissionScope;
  memberIds: Set<string>;
  /** 团队授权目前还不能在此表单里编辑，但复制 Agent 时必须保留。 */
  teamIds: Set<string>;
}

/** 一份全空的 Agent 创建草稿（各入口的起点）。 */
export const EMPTY_AGENT_DRAFT: AgentDraft = {
  name: "",
  description: "",
  instructions: "",
  conversationStarters: [],
  avatarUrl: null,
  runtimeId: "",
  model: "",
  thinkingLevel: "",
  serviceTier: "",
  skillIds: new Set(),
  permissionScope: "private",
  memberIds: new Set(),
  teamIds: new Set(),
};

/**
 * 草稿里那些"只有配合当前选中的 runtime + model 才有意义"的字段。
 * 换 runtime 会让这三个全部失效；换 model 则失效挂在该精确 model 目录下的两个。
 * 把它们放在一起，才能阻止一份孤儿 `thinking_level` / `service_tier` 为一个从未
 * 声明过它的 model 被持久化——否则 daemon 执行时会静默回退，设置页展示的与实际
 * 跑的不一致（MUL-5390）。
 * @param draft 当前草稿。
 * @param runtimeId 新选中的运行时 id。
 * @returns 换过运行时、清空 model 相关字段的新草稿。
 */
export function applyDraftRuntimeChange(
  draft: AgentDraft,
  runtimeId: string,
): AgentDraft {
  return {
    ...draft,
    runtimeId,
    model: "",
    thinkingLevel: "",
    serviceTier: "",
  };
}

/** 换 model：丢弃依赖该 model 的能力覆盖项，保留 runtime 本身。 */
export function applyDraftModelChange(
  draft: AgentDraft,
  model: string,
): AgentDraft {
  if (model === draft.model) return draft;
  return { ...draft, model, thinkingLevel: "", serviceTier: "" };
}

/**
 * 镜像服务器的 `utf8.RuneCountInString` 检查（agent.go:1013）。textarea 的
 * `maxLength` 能拦住打字和粘贴，但复制出来的 Agent 或 AI 构建器的草稿可能被程序化
 * 地种进更长的值——没有这层检查，点创建会必然撞上 400。
 */
export function isDraftDescriptionWithinLimit(description: string): boolean {
  return [...description].length <= AGENT_DESCRIPTION_MAX_LENGTH;
}

/** 按草稿的权限范围生成调用目标列表（private 为空、workspace 为全空间、
 *  members 为勾选的成员+团队）。 */
export function buildInvocationTargets(
  draft: AgentDraft,
): AgentInvocationTargetInput[] {
  if (draft.permissionScope === "private") return [];
  if (draft.permissionScope === "workspace") {
    return [{ target_type: "workspace" }];
  }
  return [
    ...[...draft.memberIds].map((targetId) => ({
      target_type: "member" as const,
      target_id: targetId,
    })),
    ...[...draft.teamIds].map((targetId) => ({
      target_type: "team" as const,
      target_id: targetId,
    })),
  ];
}

/** 从被复制的 Agent 的线上字段，还原出草稿的访问控制部分（private/workspace/成员列表）。 */
export function deriveDuplicateAccess(
  agent: Pick<Agent, "permission_mode" | "invocation_targets">,
): Pick<AgentDraft, "permissionScope" | "memberIds" | "teamIds"> {
  if (agent.permission_mode !== "public_to") {
    return {
      permissionScope: "private",
      memberIds: new Set(),
      teamIds: new Set(),
    };
  }

  const targets = agent.invocation_targets ?? [];
  if (targets.some((target) => target.target_type === "workspace")) {
    return {
      permissionScope: "workspace",
      memberIds: new Set(),
      teamIds: new Set(),
    };
  }

  const memberIds = targets
    .filter((target) => target.target_type === "member" && target.target_id)
    .map((target) => target.target_id as string);
  const teamIds = targets
    .filter((target) => target.target_type === "team" && target.target_id)
    .map((target) => target.target_id as string);
  // 名义上是 public_to 却没有成员也没有团队：视同 private 更安全。
  if (memberIds.length === 0 && teamIds.length === 0) {
    return {
      permissionScope: "private",
      memberIds: new Set(),
      teamIds: new Set(),
    };
  }
  return {
    permissionScope: "members",
    memberIds: new Set(memberIds),
    teamIds: new Set(teamIds),
  };
}

/**
 * 用"被复制的 Agent"为创建草稿做种子填充。
 *
 * `model` / `thinkingLevel` / `serviceTier` 只在它们当初被选中的那个 runtime 上有意义，
 * 所以只有副本留在完全相同的 runtime 上时才一并带过去。源 runtime 已消失、属于别人的
 * 私有设备或离线时，草稿回退到另一个 runtime，并把这三项清空让用户重选——与
 * `multica agent copy` 在服务端强制的规则一致（cmd_agent_copy.go）。MUL-5390 之前，
 * 回退会保留源的 `model`，静默持久化一个跨提供商的取值。
 */
export function buildDuplicateDraft(
  source: Agent,
  options: {
    runtimes: RuntimeDevice[];
    currentUserId: string | null;
    fallbackRuntimeId: string;
    nameSuffix: string;
  },
): AgentDraft {
  // 只有"源 runtime 还在、当前用户可用它"时才沿用它。
  const keepsRuntime =
    !!source.runtime_id &&
    options.runtimes.some(
      (runtime) =>
        runtime.id === source.runtime_id &&
        isRuntimeUsableForUser(runtime, options.currentUserId),
    );
  return {
    ...EMPTY_AGENT_DRAFT,
    name: `${source.name}${options.nameSuffix}`,
    description: source.description ?? "",
    instructions: source.instructions ?? "",
    conversationStarters: (source.conversation_starters ?? []).map((item) => ({ ...item })),
    avatarUrl: source.avatar_url ?? null,
    runtimeId: keepsRuntime
      ? (source.runtime_id as string)
      : options.fallbackRuntimeId,
    model: keepsRuntime ? source.model ?? "" : "",
    thinkingLevel: keepsRuntime ? source.thinking_level ?? "" : "",
    serviceTier: keepsRuntime ? source.service_tier ?? "" : "",
    skillIds: new Set(source.skills.map((skill) => skill.id)),
    ...deriveDuplicateAccess(source),
  };
}

/**
 * 组装 `POST /api/agents` 的请求体。空的执行覆盖项用"省略"而不是 `""` 发送，
 * 让 runtime 解析出自己的默认；仅复制需要的字段是与 runtime 无关的（`custom_args`、
 * 并发数）——镜像 `multica agent copy` 的行为。
 */
export function buildCreateAgentRequest(options: {
  draft: AgentDraft;
  runtimeId: string;
  /** 创建来源标记，用于 `agent_created` 分析事件。 */
  template?: string;
  duplicateSource?: Agent | null;
}): CreateAgentRequest {
  const { draft, runtimeId, template, duplicateSource } = options;
  const request: CreateAgentRequest = {
    name: draft.name.trim(),
    description: draft.description.trim(),
    instructions: draft.instructions.trim() || undefined,
    ...(draft.conversationStarters.length > 0
      ? {
          conversation_starters: draft.conversationStarters.map((item) => ({
            label: item.label.trim(),
            prompt: item.prompt.trim(),
          })),
        }
      : {}),
    avatar_url: draft.avatarUrl ?? undefined,
    runtime_id: runtimeId,
    model: draft.model.trim() || undefined,
    thinking_level: draft.thinkingLevel.trim() || undefined,
    service_tier: draft.serviceTier.trim() || undefined,
    permission_mode:
      draft.permissionScope === "private" ? "private" : "public_to",
    invocation_targets: buildInvocationTargets(draft),
    skill_ids: [...draft.skillIds],
    template,
  };
  if (duplicateSource) {
    // 仅复制来源：带过来它的自定义参数与合法范围内的并发上限。
    if (duplicateSource.custom_args.length > 0) {
      request.custom_args = duplicateSource.custom_args;
    }
    const sourceConcurrency = duplicateSource.max_concurrent_tasks;
    if (
      Number.isInteger(sourceConcurrency) &&
      sourceConcurrency >= AGENT_MAX_CONCURRENT_TASKS_MIN &&
      sourceConcurrency <= AGENT_MAX_CONCURRENT_TASKS_MAX
    ) {
      request.max_concurrent_tasks = sourceConcurrency;
    }
  }
  return request;
}
