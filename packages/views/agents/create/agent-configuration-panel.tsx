"use client";

// 本文件是创建 Agent（手动页与 builder 右侧实时草稿共用）的整份配置面板：
// 身份（头像/名字/描述）、行为（指令/开场白/技能）、执行（运行环境/模型/推理档位/
// 档位）、访问权限（三档 + 成员名单）。builder 场景还支持“重绑服务端 carrier”。

import { useEffect, useRef, type ReactNode } from "react";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  applyDraftModelChange,
  applyDraftRuntimeChange,
  type AgentDraft,
  type AgentPermissionScope,
} from "@multica/core/agents";
import { useConfigStore } from "@multica/core/config";
import type { MemberWithUser, RuntimeDevice } from "@multica/core/types";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { CharCounter } from "../components/char-counter";
import { ServiceTierSettingField } from "../components/inspector/service-tier-setting-field";
import { ThinkingSettingField } from "../components/inspector/thinking-prop-row";
import { ModelDropdown } from "../components/model-dropdown";
import { RuntimePicker } from "../components/runtime-picker";
import { SkillMultiSelect } from "../components/skill-multi-select";
import { ConversationStartersEditor } from "../components/conversation-starters-editor";

const PERMISSION_SCOPES: AgentPermissionScope[] = [
  "private",
  "workspace",
  "members",
];

/**
 * 创建 Agent 的配置面板：身份 / 行为 / 执行 / 访问四个分区，所有字段都落在
 * AgentDraft 草稿上（受控）。
 * @param compact 紧凑版（builder 右侧栏）布局。
 * @param onRuntimeSelect 提供时用于“builder 重绑服务端 carrier”；普通创建流程
 *   不传，草稿是唯一存在的状态，改草稿即可。
 * @param runtimeSwitchPending 有 builder 回复在途，服务端会拒绝重绑。
 * @param runtimeSwitchInFlight 已有一次重绑请求在途。
 */
export function AgentConfigurationPanel({
  draft,
  onChange,
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  nameError,
  onNameChange,
  compact = false,
  onRuntimeSelect,
  runtimeSwitchPending = false,
  runtimeSwitchInFlight = false,
}: {
  draft: AgentDraft;
  onChange: (draft: AgentDraft) => void;
  runtimes: RuntimeDevice[];
  runtimesLoading: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  nameError: string | null;
  onNameChange: (name: string) => void;
  compact?: boolean;
  /** builder 会话要重绑服务端 carrier，而不只是改草稿。普通创建流程不传——
   *  那里草稿是唯一存在的状态。 */
  onRuntimeSelect?: (runtimeId: string) => void;
  /** 有 builder 回复在途，服务端会拒绝这次重绑。 */
  runtimeSwitchPending?: boolean;
  /** 已有一条重绑请求在途。 */
  runtimeSwitchInFlight?: boolean;
}) {
  const { t } = useT("agents");
  const conversationStartersSupported = useConfigStore(
    (state) => state.agentConversationStartersSupported,
  );
  const selectedRuntime =
    runtimes.find((runtime) => runtime.id === draft.runtimeId) ?? null;
  const set = <K extends keyof AgentDraft>(key: K, value: AgentDraft[K]) =>
    onChange({ ...draft, [key]: value });
  const otherMembers = members.filter(
    (member) => member.user_id !== currentUserId,
  );
  const runtimeLocked = runtimeSwitchPending || runtimeSwitchInFlight;
  const handleRuntimeSelect = (id: string) => {
    if (id === draft.runtimeId) return;
    if (onRuntimeSelect) {
      onRuntimeSelect(id);
      return;
    }
    // 模型按运行环境区分；换运行环境时一并清掉按模型绑定的推理/档位覆盖，
    // 让新机器解析自己的默认，而不是沿用一批过期值。
    onChange(applyDraftRuntimeChange(draft, id));
  };

  return (
    <div className={cn("space-y-8", compact && "space-y-6")}>
      <SettingsSection
        title={t(($) => $.creation_studio.sections.identity)}
        description={t(($) => $.creation_studio.sections.identity_hint)}
      >
        <SettingsCard>
          <DraftFieldRow
            compact={compact}
            label={t(($) => $.create_dialog.avatar.change_aria)}
          >
            <div className={cn(!compact && "sm:flex sm:justify-end")}>
              <AvatarUploadControl
                variant="agent"
                value={draft.avatarUrl}
                name={draft.name}
                size={compact ? 52 : 56}
                onUploaded={(url) => set("avatarUrl", url)}
                onEmojiSelected={(value) => set("avatarUrl", value)}
                onClear={() => set("avatarUrl", null)}
              />
            </div>
          </DraftFieldRow>
          <AgentNameField
            compact={compact}
            name={draft.name}
            error={nameError}
            onChange={onNameChange}
          />
          <DraftFieldRow
            compact={compact}
            align="start"
            label={t(($) => $.create_dialog.description_label)}
            htmlFor="agent-create-description"
          >
            <div>
              <Textarea
                id="agent-create-description"
                name="agent-description"
                autoComplete="off"
                aria-label={t(($) => $.create_dialog.description_label)}
                value={draft.description}
                onChange={(event) => set("description", event.target.value)}
                placeholder={t(($) => $.create_dialog.description_placeholder)}
                rows={compact ? 3 : 4}
                // 创建 API 对 >255 字符的输入直接回 400。限制输入长度并展示计数器，
                // 让上限在提交前就可见——与设置页一致。
                maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                className="resize-y"
              />
              <CharCounter
                length={[...draft.description].length}
                max={AGENT_DESCRIPTION_MAX_LENGTH}
              />
            </div>
          </DraftFieldRow>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.creation_studio.sections.behavior)}
        description={t(($) => $.creation_studio.sections.behavior_hint)}
      >
        <SettingsCard>
          <DraftFieldRow
            compact
            label={t(($) => $.create_dialog.instructions.label)}
            htmlFor="agent-create-instructions"
          >
            <Textarea
              id="agent-create-instructions"
              name="agent-instructions"
              autoComplete="off"
              aria-label={t(($) => $.create_dialog.instructions.label)}
              value={draft.instructions}
              onChange={(event) => set("instructions", event.target.value)}
              placeholder={t(
                ($) => $.create_dialog.instructions.editor_placeholder,
              )}
              rows={compact ? 9 : 12}
              className="min-h-44 resize-y font-mono text-label leading-6"
            />
          </DraftFieldRow>
          {conversationStartersSupported ? (
            <div className="px-4 py-4">
              <ConversationStartersEditor
                value={draft.conversationStarters}
                onChange={(value) => set("conversationStarters", value)}
              />
            </div>
          ) : null}
          <div className="px-4 py-4">
            <SkillMultiSelect
              selectedIds={draft.skillIds}
              onChange={(ids) => set("skillIds", ids)}
            />
          </div>
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.creation_studio.sections.execution)}
        description={t(($) => $.creation_studio.sections.execution_hint)}
      >
        <SettingsCard>
          <div
            className={cn("grid gap-4 px-4 py-4", !compact && "sm:grid-cols-2")}
          >
            <div className="min-w-0">
              <RuntimePicker
                runtimes={runtimes}
                runtimesLoading={runtimesLoading}
                members={members}
                currentUserId={currentUserId}
                selectedRuntimeId={draft.runtimeId}
                onSelect={handleRuntimeSelect}
                disabled={runtimeLocked}
              />
              {/* 静默置灰的选择器是这一切里最糟的版本：用户恰恰是在当前运行环境出
                  问题时才去够它——所以告诉他什么能解锁，而不是一味拒绝。 */}
              {runtimeSwitchPending && (
                <p className="mt-1.5 text-caption text-muted-foreground">
                  {t(($) => $.creation_studio.builder.switch_runtime_pending)}
                </p>
              )}
            </div>
            <ModelDropdown
              runtimeId={selectedRuntime?.id ?? null}
              runtimeOnline={selectedRuntime?.status === "online"}
              value={draft.model}
              onChange={(value) => onChange(applyDraftModelChange(draft, value))}
              // 成功的重绑会清空模型，所以重绑在途时做的修改会被悄悄丢弃。
              disabled={!selectedRuntime || runtimeSwitchInFlight}
            />
          </div>
          {/* 两个字段都失败关闭：只有精确选中模型的实时目录宣称具备该能力时（或
              已有值需要被清掉时）才渲染——离线运行环境、发现失败或“跟随运行时默认”
              的空模型，都显示空白而不是一个兑现不了的输入框。与设置页同一套
              失败关闭规则，也就不会发出 daemon 拒绝兑现的值。 */}
          <AgentExecutionOverrides
            draft={draft}
            runtime={selectedRuntime}
            disabled={runtimeLocked}
            onChange={onChange}
          />
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.creation_studio.sections.access)}
        description={t(($) => $.creation_studio.sections.access_hint)}
      >
        <SettingsCard>
          <div
            className="space-y-1 p-2"
            role="radiogroup"
            aria-label={t(($) => $.creation_studio.sections.access)}
          >
            {PERMISSION_SCOPES.map((scope) => (
              <button
                key={scope}
                type="button"
                role="radio"
                aria-checked={draft.permissionScope === scope}
                onClick={() => set("permissionScope", scope)}
                className={cn(
                  "flex w-full items-start gap-3 rounded-md px-3 py-2.5 text-left transition-colors",
                  "hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  draft.permissionScope === scope && "bg-muted",
                )}
              >
                <span
                  className={cn(
                    "mt-1 flex size-3.5 shrink-0 items-center justify-center rounded-full border",
                    draft.permissionScope === scope && "border-primary",
                  )}
                  aria-hidden="true"
                >
                  {draft.permissionScope === scope ? (
                    <span className="size-1.5 rounded-full bg-primary" />
                  ) : null}
                </span>
                <span className="min-w-0">
                  <span className="block text-body font-medium">
                    {t(($) => $.creation_studio.access[scope].title)}
                  </span>
                  <span className="mt-0.5 block text-caption leading-5 text-muted-foreground">
                    {t(($) => $.creation_studio.access[scope].description)}
                  </span>
                </span>
              </button>
            ))}
          </div>
          {draft.permissionScope === "members" ? (
            <div className="max-h-48 overflow-y-auto p-2">
              {otherMembers.map((member) => {
                const checked = draft.memberIds.has(member.user_id);
                return (
                  <label
                    key={member.user_id}
                    className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 hover:bg-muted"
                  >
                    <Checkbox
                      checked={checked}
                      onCheckedChange={(value) => {
                        const next = new Set(draft.memberIds);
                        if (value === true) next.add(member.user_id);
                        else next.delete(member.user_id);
                        set("memberIds", next);
                      }}
                    />
                    <ActorAvatar
                      actorType="member"
                      actorId={member.user_id}
                      size="sm"
                    />
                    <span className="min-w-0 flex-1 truncate text-body">
                      {member.name}
                    </span>
                  </label>
                );
              })}
              {draft.memberIds.size === 0 ? (
                <p className="px-2 py-1 text-caption text-destructive">
                  {t(($) => $.creation_studio.access.members.required)}
                </p>
              ) : null}
            </div>
          ) : null}
        </SettingsCard>
      </SettingsSection>
    </div>
  );
}

/** Agent 名字输入行：有错时聚焦并全选文本，方便直接重敲。 */
export function AgentNameField({
  name,
  error,
  onChange,
  compact = false,
}: {
  name: string;
  error: string | null;
  onChange: (name: string) => void;
  compact?: boolean;
}) {
  const { t } = useT("agents");
  const errorId = "agent-create-name-error";
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!error) return;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [error]);

  return (
    <DraftFieldRow
      compact={compact}
      label={t(($) => $.create_dialog.name_label)}
      htmlFor="agent-create-name"
    >
      <div className="space-y-1.5">
        <Input
          ref={inputRef}
          id="agent-create-name"
          name="agent-name"
          autoComplete="off"
          aria-label={t(($) => $.create_dialog.name_label)}
          aria-invalid={!!error}
          aria-describedby={error ? errorId : undefined}
          value={name}
          onChange={(event) => onChange(event.target.value)}
          placeholder={t(($) => $.create_dialog.name_placeholder)}
        />
        {error ? (
          <p id={errorId} className="text-caption text-destructive">
            {error}
          </p>
        ) : null}
      </div>
    </DraftFieldRow>
  );
}

/**
 * 创建流程里的按模型执行覆盖（推理档位 + Codex 档位）。能力来自“精确选中模型”在
 * 所选运行环境上的实时目录，所以目录解析不了时什么都不渲染——离线运行环境、发现
 * 失败、或“跟随运行时默认”的空模型。与设置页同一套失败关闭规则，因此绝不会发出
 * daemon 拒绝兑现的值。
 */
export function AgentExecutionOverrides({
  draft,
  runtime,
  disabled = false,
  onChange,
}: {
  draft: AgentDraft;
  runtime: RuntimeDevice | null;
  disabled?: boolean;
  onChange: (draft: AgentDraft) => void;
}) {
  const { t } = useT("agents");
  const runtimeOnline = runtime?.status === "online";
  return (
    <>
      <ThinkingSettingField
        label={t(($) => $.creation_studio.thinking_label)}
        runtimeId={runtime?.id ?? null}
        runtimeOnline={runtimeOnline}
        provider={runtime?.provider ?? ""}
        model={draft.model}
        value={draft.thinkingLevel}
        canEdit={!disabled}
        onChange={(thinkingLevel) => onChange({ ...draft, thinkingLevel })}
      />
      <ServiceTierSettingField
        label={t(($) => $.creation_studio.speed_label)}
        runtimeId={runtime?.id ?? null}
        runtimeOnline={runtimeOnline}
        provider={runtime?.provider ?? ""}
        model={draft.model}
        value={draft.serviceTier}
        canEdit={!disabled}
        onChange={(serviceTier) => onChange({ ...draft, serviceTier })}
      />
    </>
  );
}

/** 设置卡里一行字段的布局容器：左侧标签（可 for 关联）、右侧控件；compact 纵向堆叠。 */
function DraftFieldRow({
  label,
  children,
  compact = false,
  align = "center",
  htmlFor,
}: {
  label: string;
  children: ReactNode;
  compact?: boolean;
  align?: "center" | "start";
  htmlFor?: string;
}) {
  return (
    <div
      className={cn(
        "gap-3 px-4 py-4",
        compact
          ? "flex flex-col"
          : "grid sm:grid-cols-[minmax(0,1fr)_minmax(280px,1.2fr)] sm:gap-8",
        !compact && (align === "center" ? "sm:items-center" : "sm:items-start"),
      )}
    >
      {htmlFor ? (
        <label htmlFor={htmlFor} className="text-body font-medium">
          {label}
        </label>
      ) : (
        <div className="text-body font-medium">{label}</div>
      )}
      <div className="min-w-0">{children}</div>
    </div>
  );
}
