"use client";

// 本文件是 Agent 设置页里的“访问权限”分区，把权限模式、调用目标、可见性等
// 集中到一个 AccessPicker 里编辑。

import type { Agent, MemberWithUser } from "@multica/core/types";
import {
  SettingsCard,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { AccessPicker } from "./inspector/access-picker";

/**
 * Agent 访问权限设置分区：包装一个 AccessPicker，把 Agent 的权限模式、
 * 调用目标、可见性等字段集中呈现并支持编辑。
 *
 * @param agent 当前正在编辑的 Agent
 * @param members 工作区内可选为调用/可见对象的成员列表
 * @param currentUserId 当前登录用户 id，用于判断是否有编辑权限
 * @param onDirtyChange 脏状态变化回调，向上层报告表单是否被改动
 * @param onUpdate 提交更新回调，接收 Agent id 与要写入的字段对象
 */
export function AgentAccessSettings({
  agent,
  members,
  currentUserId,
  onDirtyChange,
  onUpdate,
}: {
  agent: Agent;
  members: MemberWithUser[];
  currentUserId: string | null;
  onDirtyChange?: (dirty: boolean) => void;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");

  return (
    <SettingsSection
      title={t(($) => $.access.section_title)}
      description={t(($) => $.inspector.section_access_hint)}
    >
      <SettingsCard>
        <AccessPicker
          permissionMode={agent.permission_mode}
          invocationTargets={agent.invocation_targets}
          visibility={agent.visibility}
          members={members}
          ownerId={agent.owner_id}
          canEdit={
            currentUserId !== null && agent.owner_id === currentUserId
          }
          hasComposioAllowlist={
            (agent.composio_toolkit_allowlist ?? []).length > 0
          }
          onDirtyChange={onDirtyChange}
          onChange={(next) => onUpdate(agent.id, next)}
        />
      </SettingsCard>
    </SettingsSection>
  );
}
