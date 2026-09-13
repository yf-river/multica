"use client";

// 本文件是 Agent 的“访问权限”选择器：三个档位（仅自己 / 整个工作区 / 指定成员）+
// 成员勾选。可见性变化是安全敏感操作，所以采用“草稿优先”模式——选择只改本地草稿，
// 必须由拥有者显式点保存才真正落库。也被“批量设置访问范围”弹窗复用。

import { useEffect, useMemo, useRef, useState } from "react";
import { Globe, Loader2, Lock, Users } from "lucide-react";
import type {
  AgentInvocationTarget,
  AgentInvocationTargetInput,
  AgentPermissionMode,
  AgentVisibility,
  MemberWithUser,
} from "@multica/core/types";
import { effectiveAccessScope } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { ActorAvatar } from "../../../common/actor-avatar";
import { useT } from "../../../i18n";

/** 一次访问设置变更的可提交载荷：权限模式 + 调用目标列表。 */
export type AccessChange = {
  permission_mode: AgentPermissionMode;
  invocation_targets: AgentInvocationTargetInput[];
};

type AccessScope = "private" | "workspace" | "members";

/** 从后端目标列表里抽出指定类型（成员 / 团队）的目标 id 集合。 */
function selectedTargetIds(
  targets: AgentInvocationTarget[] | undefined | null,
  type: "member" | "team",
): string[] {
  return (targets ?? [])
    .filter(
      (target) => target.target_type === type && target.target_id !== null,
    )
    .map((target) => target.target_id as string);
}

/**
 * 草稿优先的访问权限编辑器。可见性变化属安全敏感操作，所以选择档位只更新草稿；
 * 直到拥有者显式保存完整选择，才真正持久化。
 */
export function AccessPicker({
  permissionMode,
  invocationTargets,
  visibility: _visibility,
  members,
  ownerId,
  canEdit = true,
  hasComposioAllowlist = false,
  onDirtyChange,
  onReadyChange,
  onChange,
  hideFooter = false,
}: {
  permissionMode: AgentPermissionMode;
  invocationTargets: AgentInvocationTarget[] | undefined;
  visibility: AgentVisibility;
  members: MemberWithUser[];
  ownerId?: string | null;
  canEdit?: boolean;
  hasComposioAllowlist?: boolean;
  onDirtyChange?: (dirty: boolean) => void;
  /** 草稿变为“可提交/不可提交”时通知。可提交时的第二个参数是当前 AccessChange，
   *  父层可存下来立即使用（如批量弹窗的“应用”按钮）。 */
  onReadyChange?: (ready: boolean, change?: AccessChange) => void;
  onChange?: (next: AccessChange) => Promise<void> | void;
  /** true 时隐藏底部“保存”栏——由父弹窗自己持有提交触发器
   *  （如批量“设置访问范围”弹窗）。 */
  hideFooter?: boolean;
}) {
  const { t } = useT("agents");
  const { t: tc } = useT("common");
  // 集中推导（MUL-3963）：把规范档位映射到选择器的本地草稿键（private / workspace / members）。
  const canonical = effectiveAccessScope(permissionMode, invocationTargets);
  const persistedPrivate = permissionMode === "private";
  const persistedWorkspace = canonical === "workspace";
  const persistedScope: AccessScope = persistedPrivate
    ? "private"
    : persistedWorkspace
      ? "workspace"
      : "members";
  const persistedMembers = useMemo(
    () => selectedTargetIds(invocationTargets, "member"),
    [invocationTargets],
  );
  const teamIds = useMemo(
    () => selectedTargetIds(invocationTargets, "team"),
    [invocationTargets],
  );

  const [draftScope, setDraftScope] = useState<AccessScope>(persistedScope);
  const [draftMembers, setDraftMembers] = useState(persistedMembers);
  const [saving, setSaving] = useState(false);
  /** 记录用户是否显式选过档位（点过任一单选框）。一旦为 true，即使所选与持久化
   *  默认一致，选择器也认为草稿“就绪”——对批量选择应用默认档位同样是合法动作。 */
  const [hasInteracted, setHasInteracted] = useState(false);

  /** 包装函数：标记“已交互”并更新草稿档位。 */
  const selectDraftScope = (scope: AccessScope) => {
    setHasInteracted(true);
    setDraftScope(scope);
  };

  // 只有“持久化的值真的变了”才重置草稿（按值比较，不按引用）。父层传类似
  // invocationTargets={[]} 会在每次渲染都新建数组，若按引用比较，这个 effect
  // 就会每次渲染都触发，把用户正在编辑的内容冲掉。
  const prevPersistedScopeRef = useRef(persistedScope);
  const prevPersistedMembersRef = useRef(persistedMembers);
  useEffect(() => {
    const scopeChanged = persistedScope !== prevPersistedScopeRef.current;
    const membersChanged =
      persistedMembers.length !== prevPersistedMembersRef.current.length ||
      persistedMembers.some(
        (id, i) => id !== prevPersistedMembersRef.current[i],
      );
    if (scopeChanged || membersChanged) {
      setDraftScope(persistedScope);
      setDraftMembers(persistedMembers);
      prevPersistedScopeRef.current = persistedScope;
      prevPersistedMembersRef.current = persistedMembers;
    }
  }, [persistedScope, persistedMembers]);

  const editableMembers = ownerId
    ? members.filter((member) => member.user_id !== ownerId)
    : members;

  const sameMembers =
    draftMembers.length === persistedMembers.length &&
    draftMembers.every((id) => persistedMembers.includes(id));
  const dirty =
    draftScope !== persistedScope ||
    (draftScope === "members" && !sameMembers);
  const hasMemberTarget = draftMembers.length > 0;

  useEffect(() => {
    onDirtyChange?.(dirty);
    return () => onDirtyChange?.(false);
  }, [dirty, onDirtyChange]);

  /** 当前草稿要提交的变更；草稿不可提交时（“指定成员”却一个都没选）为 null。
   *  按草稿值 memo：父层把该对象存进 state 再回传本组件时引用不变，
   *  下面的通知 effect 因此保持安静、不会死循环。 */
  const draftChange = useMemo<AccessChange | null>(() => {
    if (draftScope === "private") {
      return { permission_mode: "private", invocation_targets: [] };
    }
    const targets: AgentInvocationTargetInput[] = [];
    if (draftScope === "workspace") {
      targets.push({ target_type: "workspace" });
    }
    if (draftScope === "members") {
      if (draftMembers.length === 0) return null;
      for (const id of draftMembers) {
        targets.push({ target_type: "member", target_id: id });
      }
      for (const id of teamIds) {
        targets.push({ target_type: "team", target_id: id });
      }
    }
    return { permission_mode: "public_to", invocation_targets: targets };
  }, [draftScope, draftMembers, teamIds]);

  /** 用户已显式选过档位（hasInteracted）且草稿可提交，才算“就绪”。
   *  不要求草稿与持久化状态不同——对批量选择应用默认档位同样是合法动作。 */
  const readyChange = hasInteracted ? draftChange : null;

  // 只在值变化时通知，绝不在每次渲染都通知。`onReadyChange` 需由调用方保持稳定；
  // 配合 memo 过的 `readyChange`，父层把变更存进 state 后不会反复触发本 effect。
  // 刻意不做清理函数：依赖一变就去清父层状态，正是之前把它变成更新循环的原因。
  // 重置父层自己的状态由父层负责。
  useEffect(() => {
    onReadyChange?.(readyChange !== null, readyChange ?? undefined);
  }, [readyChange, onReadyChange]);

  const toggleMember = (userId: string, checked: boolean) => {
    setDraftMembers((current) => {
      const next = new Set(current);
      if (checked) next.add(userId);
      else next.delete(userId);
      return Array.from(next);
    });
  };

  const save = async () => {
    if (!onChange || saving || !draftChange) return;
    setSaving(true);
    try {
      await onChange(draftChange);
    } finally {
      setSaving(false);
    }
  };

  if (!canEdit) {
    const summaryLabel = persistedPrivate
      ? t(($) => $.access.trigger_private)
      : persistedWorkspace
        ? t(($) => $.access.trigger_workspace)
        : persistedMembers.length > 0
          ? t(($) => $.access.trigger_members_count, {
              count: persistedMembers.length,
            })
          : t(($) => $.access.trigger_members_empty);

    return (
      <div
        className="flex items-start gap-3 px-4 py-4"
        aria-label={t(($) => $.access.owner_only_readonly)}
        data-testid="access-readonly"
      >
        <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <Lock className="size-4" aria-hidden="true" />
        </span>
        <div className="min-w-0">
          <p className="text-body font-medium">{summaryLabel}</p>
          <p className="mt-0.5 text-caption leading-5 text-muted-foreground">
            {t(($) => $.access.owner_only_readonly)}
          </p>
        </div>
      </div>
    );
  }

  return (
    <fieldset>
      <legend className="sr-only">{t(($) => $.access.tooltip)}</legend>

      <div className="divide-y divide-surface-border">
        <AccessChoice
          name="agent-access-mode"
          value="private"
          icon={Lock}
          title={t(($) => $.access.private_title)}
          description={t(($) => $.access.private_desc)}
          selected={draftScope === "private"}
          onSelect={() => selectDraftScope("private")}
        />
        <AccessChoice
          name="agent-access-mode"
          value="workspace"
          icon={Globe}
          title={t(($) => $.access.workspace_title)}
          description={t(($) => $.access.workspace_desc)}
          selected={draftScope === "workspace"}
          onSelect={() => selectDraftScope("workspace")}
        />
        <AccessChoice
          name="agent-access-mode"
          value="members"
          icon={Users}
          title={t(($) => $.access.members_title)}
          description={t(($) => $.access.members_desc)}
          selected={draftScope === "members"}
          onSelect={() => selectDraftScope("members")}
        />
      </div>

      {draftScope === "members" ? (
        <div className="border-t border-surface-border bg-muted/20 px-4 py-5 sm:px-6">
          <div>
            {editableMembers.length === 0 ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.access.members_empty)}
              </p>
            ) : (
              <div className="max-h-64 divide-y divide-surface-border overflow-y-auto rounded-lg border bg-background overscroll-contain">
                {editableMembers.map((member) => {
                  const id = `agent-access-member-${member.user_id}`;
                  return (
                    <div
                      key={member.user_id}
                      className="flex items-center gap-3 px-3 py-3 hover:bg-surface-hover"
                    >
                      <Checkbox
                        id={id}
                        checked={draftMembers.includes(member.user_id)}
                        onCheckedChange={(value) =>
                          toggleMember(member.user_id, value === true)
                        }
                      />
                      <label
                        htmlFor={id}
                        className="flex min-w-0 flex-1 cursor-pointer items-center gap-3"
                      >
                        <ActorAvatar
                          actorType="member"
                          actorId={member.user_id}
                          size="sm"
                        />
                        <span className="min-w-0 flex-1 truncate text-body">
                          {member.name}
                        </span>
                      </label>
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {!hasMemberTarget ? (
            <p className="mt-3 text-caption text-destructive" role="alert">
              {t(($) => $.access.shared_target_required)}
            </p>
          ) : null}
        </div>
      ) : null}

      {hasComposioAllowlist && persistedPrivate && draftScope !== "private" ? (
        <div className="border-t border-surface-border bg-muted/20 px-4 py-4 sm:px-6">
          <p className="border-l-2 border-warning pl-3 text-caption leading-5 text-muted-foreground">
            {t(($) => $.access.composio_switch_hint)}
          </p>
        </div>
      ) : null}

      {hideFooter ? null : (
        <div className="flex justify-end border-t border-surface-border px-4 py-3.5">
          <Button
            type="button"
            onClick={() => void save()}
            disabled={
              !onChange || !dirty || saving || (draftScope === "members" && !hasMemberTarget)
            }
          >
            {saving ? (
              <Loader2
                className="size-4 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            ) : null}
            {tc(($) => $.save)}
          </Button>
        </div>
      )}
    </fieldset>
  );
}

/** 一行“单选项”：radio + 图标 + 标题/描述。点击整行即选中。 */
function AccessChoice({
  name,
  value,
  icon: Icon,
  title,
  description,
  selected,
  onSelect,
}: {
  name: string;
  value: string;
  icon: typeof Lock;
  title: string;
  description: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <label className="flex min-h-16 cursor-pointer items-start gap-3 px-4 py-3.5 transition-colors hover:bg-surface-hover">
      <input
        type="radio"
        name={name}
        value={value}
        checked={selected}
        onChange={onSelect}
        className="mt-2 size-4 shrink-0 accent-foreground"
      />
      <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <Icon className="size-4" aria-hidden="true" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-body font-medium">{title}</span>
        <span className="mt-0.5 block text-caption leading-5 text-muted-foreground">
          {description}
        </span>
      </span>
    </label>
  );
}
