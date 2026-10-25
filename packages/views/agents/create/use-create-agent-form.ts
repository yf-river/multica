"use client";

// 本文件是创建 Agent 各路由共享的表单状态 hook：一份 AgentDraft 草稿 + 表单要
// 渲染的工作区目录（运行环境 / 成员 / 技能），并给出“创建按钮能否点”的就绪判定。

import {
  useEffect,
  useMemo,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import { useQuery } from "@tanstack/react-query";
import {
  EMPTY_AGENT_DRAFT,
  isDraftDescriptionWithinLimit,
  AGENT_CONVERSATION_STARTER_LABEL_MAX_LENGTH,
  AGENT_CONVERSATION_STARTER_MAX_LENGTH,
  type AgentDraft,
} from "@multica/core/agents";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { isRuntimeUsableForUser, runtimeListOptions } from "@multica/core/runtimes";
import type {
  MemberWithUser,
  RuntimeDevice,
  SkillSummary,
} from "@multica/core/types";
import {
  memberListOptions,
  skillListOptions,
} from "@multica/core/workspace/queries";

interface CreateAgentForm {
  draft: AgentDraft;
  setDraft: Dispatch<SetStateAction<AgentDraft>>;
  runtimes: RuntimeDevice[];
  runtimesLoading: boolean;
  /** 运行环境查询已应答——无论数据还是错误。 */
  runtimesSettled: boolean;
  /** 本成员可执行、且在线的运行环境。 */
  usableRuntimes: RuntimeDevice[];
  selectedRuntime: RuntimeDevice | null;
  members: MemberWithUser[];
  workspaceSkills: SkillSummary[];
  currentUserId: string | null;
  /** 创建按钮依赖的除名字之外的全部前置条件。 */
  draftReady: boolean;
}

interface RuntimeSeed {
  /**
   * 调用方仍在解析“这条草稿属于哪台运行环境”时为 false。表单在它翻转前不播种，
   * 让“按列表序兜底”永远不会赢过权威答案的竞态。
   */
  ready: boolean;
  /** 要采用的运行环境；空串回退到第一台可用者。 */
  runtimeId: string;
  /** Live builder sessions must be explicitly rebound before adopting one. */
  fallbackToFirstUsable?: boolean;
}

const IMMEDIATE_RUNTIME_SEED: RuntimeSeed = { ready: true, runtimeId: "" };

/**
 * 创建 Agent 各路由共享的状态：一份草稿 + 表单要渲染的工作区目录。
 *
 * 草稿在这里就是普通组件 state。让它跨导航存活是调用方的活，因为两条路由答案
 * 不同：AI 路由的配置属于会话、存在服务端；手动路由的配置无处可挂、本地持久化。
 */
export function useCreateAgentForm(options?: {
  /**
   * “已存在于某处的草稿”其运行环境从哪来。恢复的 builder 会话跑在其 carrier 绑定
   * 的机器上，选择器必须显示“那台”——显示第一台可用机器正是 MUL-5163 的翻车
   * 方式：界面显示运行时 A，每条消息却都跑在 B。
   */
  runtimeSeed?: RuntimeSeed;
}): CreateAgentForm {
  const wsId = useWorkspaceId();
  const currentUser = useAuthStore((state) => state.user);
  const currentUserId = currentUser?.id ?? null;

  const {
    data: runtimes = [],
    isLoading: runtimesLoading,
    isSuccess: runtimesLoaded,
    isError: runtimesFailed,
  } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: workspaceSkills = [] } = useQuery(skillListOptions(wsId));

  const [draft, setDraft] = useState<AgentDraft>(EMPTY_AGENT_DRAFT);

  const usableRuntimes = useMemo(
    () =>
      runtimes.filter(
        (runtime) =>
          runtime.status === "online" &&
          isRuntimeUsableForUser(runtime, currentUserId),
      ),
    [currentUserId, runtimes],
  );
  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === draft.runtimeId) ?? null;

  // 给选择器播种，让草稿无需手动选择即可提交。只填空槽：一旦草稿指名了运行环境
  // ——用户选的、复制模板带来的、从服务端恢复的——谁都不许再动它。
  //
  // 用一个 effect 拍板而非两个：权威种子与按列表序的兜底在各自的时间点 resolve，
  // 放任它们竞态，正是选择器最终停在“什么都不执行的运行环境”上的方式。
  const seed = options?.runtimeSeed ?? IMMEDIATE_RUNTIME_SEED;
  const seedReady = seed.ready;
  const seedRuntimeId = seed.runtimeId;
  const fallbackToFirstUsable = seed.fallbackToFirstUsable !== false;
  useEffect(() => {
    if (draft.runtimeId || !seedReady) return;
    const next = seedRuntimeId || (fallbackToFirstUsable ? usableRuntimes[0]?.id : "") || "";
    if (!next) return;
    setDraft((current) => ({ ...current, runtimeId: next }));
  }, [draft.runtimeId, seedReady, seedRuntimeId, fallbackToFirstUsable, usableRuntimes]);

  const accessInvalid =
    draft.permissionScope === "members" &&
    draft.memberIds.size === 0 &&
    draft.teamIds.size === 0;

  const conversationStartersInvalid = draft.conversationStarters.some(
    (item) =>
      item.label.trim().length === 0 ||
      item.prompt.trim().length === 0 ||
      [...item.label].length > AGENT_CONVERSATION_STARTER_LABEL_MAX_LENGTH ||
      [...item.prompt].length > AGENT_CONVERSATION_STARTER_MAX_LENGTH,
  );

  return {
    draft,
    setDraft,
    runtimes,
    runtimesLoading,
    runtimesSettled: runtimesLoaded || runtimesFailed,
    usableRuntimes,
    selectedRuntime,
    members,
    workspaceSkills,
    currentUserId,
    draftReady:
      selectedRuntime != null &&
      isRuntimeUsableForUser(selectedRuntime, currentUserId) &&
      isDraftDescriptionWithinLimit(draft.description) &&
      !accessInvalid &&
      !conversationStartersInvalid,
  };
}
