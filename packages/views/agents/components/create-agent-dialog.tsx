"use client";

// 本文件是“创建 Agent / 复制 Agent（Duplicate）”弹窗：头像、名字、描述、访问权限
//（新版权限模型或旧版可见性）、运行环境、模型、指令、技能一应俱全，一次提交。
// 复制模式下从模板 Agent 预填所有可见字段，并透传隐藏配置（args/并发），真正克隆。

import { useState } from "react";
import { Globe, Lock, Users } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { ModelDropdown } from "./model-dropdown";
import { RuntimePicker } from "./runtime-picker";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import { InstructionsEditor } from "./instructions-editor";
import { SkillMultiSelect } from "./skill-multi-select";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useFeatureEnabled } from "@multica/core/config";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type {
  Agent,
  AgentInvocationTargetInput,
  AgentPermissionMode,
  AgentVisibility,
  RuntimeDevice,
  MemberWithUser,
  CreateAgentRequest,
} from "@multica/core/types";
import { isImeComposing } from "@multica/core/utils";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { toast } from "sonner";
import {
  AGENT_DESCRIPTION_MAX_LENGTH,
  VISIBILITY_DESCRIPTION,
  VISIBILITY_LABEL,
} from "@multica/core/agents";
import { ActorAvatar } from "../../common/actor-avatar";
import { CharCounter } from "./char-counter";
import { useT } from "../../i18n";

/**
 * 创建 / 复制 Agent 的全功能弹窗。
 * @param template 提供时进入“复制模式”：可见字段（名字/描述/运行环境/可见性/模型）
 *   从该 Agent 预填，隐藏字段（instructions / custom_args / custom_env /
 *   max_concurrent_tasks）随创建请求转发，使新 Agent 成为真克隆。技能由调用方在
 *   createAgent 成功后另行复制——它们不属于 CreateAgentRequest。
 * @param squadId 提供时，每次创建成功后跟随 addSquadMember(squadId, agent)，
 *   让新 Agent 加入该 squad。若入队调用失败，Agent 已存在，弹窗仅弹警告 toast——
 *   用户可从 Members tab 手动补加。
 * @param onCreate 返回创建的 Agent，供弹窗用它执行后续 setAgentSkills（把表单里
 *   勾的技能 id 挂上去）。没有技能区块的调用方可继续返回 void；弹窗容忍假值返回
 *   （不执行后续）。
 */
export function CreateAgentDialog({
  runtimes,
  runtimesLoading,
  members,
  currentUserId,
  template,
  squadId,
  onClose,
  onCreate,
}: {
  runtimes: RuntimeDevice[];
  runtimesLoading?: boolean;
  members: MemberWithUser[];
  currentUserId: string | null;
  // When provided, the dialog opens in "Duplicate" mode: the visible
  // fields (name / description / runtime / visibility / model) are
  // pre-populated from this agent, and the hidden fields
  // (instructions / custom_args / custom_env / max_concurrent_tasks)
  // are forwarded to the create call so the new agent is a true clone.
  // Skills are copied separately by the caller after createAgent
  // succeeds — they're not part of CreateAgentRequest.
  template?: Agent | null;
  // When set, every successful create is followed by
  // addSquadMember(squadId, agent) so the new agent joins this squad.
  // If the squad-join call fails the agent still exists and the dialog
  // surfaces a warning toast — the user can add it manually from the
  // Members tab.
  squadId?: string;
  onClose: () => void;
  // Returns the created Agent so the dialog can run a follow-up
  // setAgentSkills with the IDs the user picked in the form. Pre-skill-
  // section callers can keep returning `void`; the dialog tolerates a
  // falsy return (no follow-up runs).
  onCreate: (data: CreateAgentRequest) => Promise<Agent | void>;
}) {
  const { t } = useT("agents");
  const isDuplicate = !!template;
  const queryClient = useQueryClient();
  const wsId = useWorkspaceId();
  // MUL-4010：把 private / public_to 权限模型推广到创建流程，与详情页的
  // AccessPicker 对齐。与 Composio 发布共用 `composio_mcp_apps` 开关——MUL-3963
  // 权限模型就是为管控 Composio 分享而生，所以两个界面一起翻转。默认 OFF，
  // 让生产环境在 Composio 放行前保持旧的 Workspace / Personal 切换。
  const accessPickerEnabled = useFeatureEnabled(COMPOSIO_MCP_APPS_FLAG, false);

  // Name defaults: duplicate uses "<original> copy". Manual-create starts blank.
  const [name, setName] = useState(
    template ? `${template.name}${t(($) => $.create_dialog.duplicate_copy_suffix)}` : "",
  );
  const [description, setDescription] = useState(template?.description ?? "");
  // Legacy visibility state. Kept as the source of truth when
  // `accessPickerEnabled` is false; only used to seed the new access state
  // when the flag flips on for a duplicate.
  // 旧的可见性状态：`accessPickerEnabled` 为 false 时作为唯一真相；开启时仅用于
  // 给新权限模型播种初始值（复制场景）。
  const [visibility, setVisibility] = useState<AgentVisibility>(
    template?.visibility ?? "workspace",
  );

  // 新权限模型状态（MUL-3963 对齐）。复制时从模板播种，克隆体带着源 Agent 的
  // 授权落地；否则默认 public_to + workspace，与旧版 “Workspace” 默认等价，
  // 保证“直接点创建”和以前产出相同结果。
  const [permissionMode, setPermissionMode] = useState<AgentPermissionMode>(
    template?.permission_mode ?? "public_to",
  );
  const [workspaceTargetOn, setWorkspaceTargetOn] = useState<boolean>(() => {
    if (template) {
      return (template.invocation_targets ?? []).some(
        (tgt) => tgt.target_type === "workspace",
      );
    }
    return true;
  });
  const [selectedMemberIds, setSelectedMemberIds] = useState<Set<string>>(
    () =>
      new Set(
        (template?.invocation_targets ?? [])
          .filter((tgt) => tgt.target_type === "member" && tgt.target_id)
          .map((tgt) => tgt.target_id as string),
      ),
  );

  // 模板上的团队目标跨创建保留，避免静默丢掉选择器还没暴露的授权类型
  //（与 AccessPicker 的 `teamIds` 透传呼应）。
  const templateTeamTargets: AgentInvocationTargetInput[] = (
    template?.invocation_targets ?? []
  )
    .filter((tgt) => tgt.target_type === "team" && tgt.target_id)
    .map((tgt) => ({
      target_type: "team" as const,
      target_id: tgt.target_id as string,
    }));

  const [model, setModel] = useState(template?.model ?? "");
  const [instructions, setInstructions] = useState(template?.instructions ?? "");
  const [avatarUrl, setAvatarUrl] = useState<string | null>(template?.avatar_url ?? null);
  const [selectedSkillIds, setSelectedSkillIds] = useState<Set<string>>(
    () => new Set(template?.skills.map((s) => s.id) ?? []),
  );
  const [creating, setCreating] = useState(false);

  // 复制模式预填：克隆体落在源 Agent 的运行环境上，用户不必重选。若该运行环境现在
  // 对当前用户已锁定（创建会 403），则跳过。空串兜底把决定权交回 RuntimePicker——
  // 它持有筛选状态，只有它知道此刻哪些运行环境可见。
  const [selectedRuntimeId, setSelectedRuntimeId] = useState(() => {
    const templateRuntime = template?.runtime_id
      ? runtimes.find((r) => r.id === template.runtime_id)
      : undefined;
    if (templateRuntime && isRuntimeUsableForUser(templateRuntime, currentUserId)) {
      return templateRuntime.id;
    }
    return "";
  });

  const selectedRuntime = runtimes.find((d) => d.id === selectedRuntimeId) ?? null;
  // 纵深防御：即使一台锁定运行环境不知何故被选中（例如复制一个模板运行环境现已
  // 锁定的 Agent，而工作区又没有可用兜底），也要把 Create 拦住——不提交一个后端
  // 必然 403 的请求。
  const selectedRuntimeLocked =
    selectedRuntime != null &&
    !isRuntimeUsableForUser(selectedRuntime, currentUserId);
  const accessSelectionInvalid =
    accessPickerEnabled &&
    permissionMode === "public_to" &&
    !workspaceTargetOn &&
    selectedMemberIds.size === 0 &&
    templateTeamTargets.length === 0;

  // squad 加入的共同后续。不返回任何值——调用方已弹过“创建成功”的 toast，这里只
  // 需要在上“Agent 已落地但入队失败”时弹出警告。squad 成员列表的缓存失效一并做掉，
  // 让 Members tab 免手动刷新即可重渲染。
  const attachToSquad = async (agentId: string, displayName: string) => {
    if (!squadId) return;
    try {
      await api.addSquadMember(squadId, {
        member_type: "agent",
        member_id: agentId,
      });
      if (wsId) {
        queryClient.invalidateQueries({
          queryKey: [...workspaceKeys.squads(wsId), squadId, "members"],
        });
        queryClient.invalidateQueries({
          queryKey: [...workspaceKeys.squads(wsId), squadId],
        });
      }
    } catch (err) {
      toast.warning(
        t(($) => $.create_dialog.squad_join_failed_toast, {
          name: displayName,
          error: err instanceof Error ? err.message : "unknown error",
        }),
      );
    }
  };

  const handleSubmit = async () => {
    if (
      !name.trim() ||
      !selectedRuntime ||
      selectedRuntimeLocked ||
      accessSelectionInvalid
    ) {
      return;
    }
    setCreating(true);

    try {
      const trimmedInstructions = instructions.trim();
      const data: CreateAgentRequest = {
        name: name.trim(),
        description: description.trim(),
        runtime_id: selectedRuntime.id,
        model: model.trim() || undefined,
        instructions: trimmedInstructions || undefined,
        avatar_url: avatarUrl ?? undefined,
        skill_ids: [...selectedSkillIds],
      };
      if (accessPickerEnabled) {
        // 新版 MUL-3963 形态：发送权威的权限字段，让后端推导旧版 `visibility`。
        // 与 AccessPicker 的 `emit` 归一化一致——public_to 但零目标时塌缩为 private
        // 作为纵深防御，后端永远见不到“空 public”请求。模板的团队目标保留。
        const invocationTargets: AgentInvocationTargetInput[] = [];
        if (permissionMode === "public_to") {
          if (workspaceTargetOn) {
            invocationTargets.push({ target_type: "workspace" });
          } else {
            for (const id of selectedMemberIds) {
              invocationTargets.push({ target_type: "member", target_id: id });
            }
            for (const tgt of templateTeamTargets) {
              invocationTargets.push(tgt);
            }
          }
        }
        const collapseToPrivate =
          permissionMode === "public_to" && invocationTargets.length === 0;
        data.permission_mode = collapseToPrivate ? "private" : permissionMode;
        data.invocation_targets = collapseToPrivate ? [] : invocationTargets;
      } else {
        // 旧路径：原样发送可见性开关，后端在服务端把它映射成
        // permission_mode + invocation_targets。
        data.visibility = visibility;
      }
      if (template) {
        // 复制路径：把源 Agent 的隐藏配置字段（args/并发）转发出去，让克隆体
        // 开箱即用。技能走弹窗表单。自 MUL-2600 起 agent 资源形态不再携带
        // custom_env 值，所以复制完全无法拷贝 env——用户只能在克隆体上经
        // env tab（现已走审计过的 `/env` 端点）重新设置。弹窗的创建调用
        // 在创建时仍接受 custom_env，但源值在这里取不到。
        if (template.custom_args.length) data.custom_args = template.custom_args;
        if (template.max_concurrent_tasks) {
          data.max_concurrent_tasks = template.max_concurrent_tasks;
        }
      }
      const createdAgent = await onCreate(data);
      // Squad 上下文：等技能落地后再把 Agent 挂进 squad，让 Members tab 展示的
      // Agent 自带技能。原子性是刻意“尽力而为”（见 MUL-2178 计划）——部分失败
      // 只弹警告 toast，用户可从 Add Member 弹窗重试。
      if (createdAgent && squadId) {
        await attachToSquad(createdAgent.id, createdAgent.name);
      }
      onClose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.create_dialog.create_failed_toast));
      setCreating(false);
    }
  };

  const headerTitle = isDuplicate
    ? t(($) => $.create_dialog.title_duplicate)
    : t(($) => $.create_dialog.title_create);

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent className="p-0 gap-0 flex flex-col overflow-hidden !top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2 !w-full !max-w-2xl !h-[85vh]">
        <DialogHeader className="border-b px-5 py-3 space-y-0">
          <DialogTitle className="text-title-sm font-semibold">{headerTitle}</DialogTitle>
          {isDuplicate && template && (
            <DialogDescription className="mt-1 text-caption">
              {t(($) => $.create_dialog.description_duplicate, { name: template.name })}
            </DialogDescription>
          )}
          {!isDuplicate && (
            <DialogDescription className="mt-1 text-caption">
              {t(($) => $.create_dialog.description_create)}
            </DialogDescription>
          )}
        </DialogHeader>

        <div className="flex-1 overflow-y-auto p-5">
          <div className="space-y-4 min-w-0">
            {/* 身份行：头像（左）+ 名字与描述堆叠（右）。头像在视觉上锚定用户正创建的
                对象身份；与名字字段并排放，读起来就是“这是这个 Agent 的脸 + 名字”，
                跟详情页头部同构，让人瞬间眼熟。 */}
            <div className="flex items-start gap-4">
              <AvatarUploadControl
                variant="agent"
                value={avatarUrl}
                name={name}
                size={64}
                onUploaded={setAvatarUrl}
                onEmojiSelected={setAvatarUrl}
                onClear={() => setAvatarUrl(null)}
              />
              <div className="flex-1 min-w-0 space-y-3">
                <div>
                  <Label className="text-caption text-muted-foreground">{t(($) => $.create_dialog.name_label)}</Label>
                  <Input
                    autoFocus
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t(($) => $.create_dialog.name_placeholder)}
                    className="mt-1"
                    onKeyDown={(e) => {
                      if (isImeComposing(e)) return;
                      if (e.key === "Enter") handleSubmit();
                    }}
                  />
                </div>

                <div>
                  <Label className="text-caption text-muted-foreground">{t(($) => $.create_dialog.description_label)}</Label>
                  <Input
                    type="text"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder={t(($) => $.create_dialog.description_placeholder)}
                    maxLength={AGENT_DESCRIPTION_MAX_LENGTH}
                    className="mt-1"
                  />
                  <div className="mt-1">
                    <CharCounter
                      length={[...description].length}
                      max={AGENT_DESCRIPTION_MAX_LENGTH}
                    />
                  </div>
                </div>
              </div>
            </div>

            {accessPickerEnabled ? (
              <AccessSection
                permissionMode={permissionMode}
                onPermissionModeChange={setPermissionMode}
                workspaceTargetOn={workspaceTargetOn}
                onWorkspaceTargetChange={setWorkspaceTargetOn}
                selectedMemberIds={selectedMemberIds}
                onSelectedMemberIdsChange={setSelectedMemberIds}
                members={members}
                currentUserId={currentUserId}
              />
            ) : (
              <div>
                <Label className="text-caption text-muted-foreground">{t(($) => $.create_dialog.visibility_label)}</Label>
                <div className="mt-1.5 flex gap-2">
                  <button
                    type="button"
                    onClick={() => setVisibility("workspace")}
                    className={`flex flex-1 items-center gap-2 rounded-lg border px-3 py-2.5 text-body transition-colors ${
                      visibility === "workspace"
                        ? "border-primary bg-primary/5"
                        : "border-border hover:bg-muted"
                    }`}
                  >
                    <Globe className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <div className="text-left">
                      <div className="font-medium">{VISIBILITY_LABEL.workspace}</div>
                      <div className="text-caption text-muted-foreground">
                        {VISIBILITY_DESCRIPTION.workspace}
                      </div>
                    </div>
                  </button>
                  <button
                    type="button"
                    onClick={() => setVisibility("private")}
                    className={`flex flex-1 items-center gap-2 rounded-lg border px-3 py-2.5 text-body transition-colors ${
                      visibility === "private"
                        ? "border-primary bg-primary/5"
                        : "border-border hover:bg-muted"
                    }`}
                  >
                    <Lock className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <div className="text-left">
                      <div className="font-medium">{VISIBILITY_LABEL.private}</div>
                      <div className="text-caption text-muted-foreground">
                        {VISIBILITY_DESCRIPTION.private}
                      </div>
                    </div>
                  </button>
                </div>
              </div>
            )}

            <RuntimePicker
              runtimes={runtimes}
              runtimesLoading={runtimesLoading}
              members={members}
              currentUserId={currentUserId}
              selectedRuntimeId={selectedRuntimeId}
              onSelect={(id) => {
                // 模型是按运行环境的；为旧运行环境选的值新运行环境上可能不存在，
                // 所以切换运行环境时把它清掉。
                if (id !== selectedRuntimeId) setModel("");
                setSelectedRuntimeId(id);
              }}
            />

            <ModelDropdown
              runtimeId={selectedRuntime?.id ?? null}
              runtimeOnline={selectedRuntime?.status === "online"}
              value={model}
              onChange={setModel}
              disabled={!selectedRuntime}
            />

            {/* --- 可选分区（指令 / 技能）---
                默认收起，保证快速创建不被拖慢。
                复制模式会从源 Agent 预填一切。 */}
            <InstructionsEditor
              value={instructions}
              onChange={setInstructions}
              placeholder={
                isDuplicate
                  ? t(($) => $.create_dialog.instructions.placeholder_duplicate)
                  : t(($) => $.create_dialog.instructions.placeholder_blank)
              }
            />

            <SkillMultiSelect
              selectedIds={selectedSkillIds}
              onChange={setSelectedSkillIds}
            />
          </div>
        </div>

        {/* 用行内底部栏而不是 <DialogFooter>：它自带 `-mx-4 -mb-4`，那是针对默认
            `p-4` 内边距的 DialogContent 的。本弹窗的 DialogContent 用 `p-0`，
            负边距会把底部栏推出弹窗。普通 flex 行 + `border-t` 锚定，
            视觉节奏不变又不会有溢出 bug。 */}
        <div className="flex items-center justify-end gap-2 border-t bg-background px-5 py-3">
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.create_dialog.cancel)}
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={
              creating || !name.trim() || !selectedRuntime || selectedRuntimeLocked
            }
            title={
              selectedRuntimeLocked
                ? t(($) => $.create_dialog.runtime_private_locked_tooltip)
                : undefined
            }
          >
            {creating ? t(($) => $.create_dialog.creating) : t(($) => $.create_dialog.create)}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}


/**
 * 创建/复制流程里的内联权限编辑器，由 `COMPOSIO_MCP_APPS_FLAG` 开关控制。
 * 语义与详情页 `AccessPicker` 对齐：底层模型是 `permission_mode` +
 * `invocation_targets`（MUL-3963），而不是旧版 `visibility`。
 *
 * 三个互斥档位与详情页 AccessPicker 一致。“指定成员”展开成员白名单；
 * “整个工作区”授权之下不再叠冗余的成员目标。
 *
 * 当前查看者被刻意排除在成员列表外：拥有者永远能调用自己的 Agent，
 * 再列一遍反而误导。
 */
function AccessSection({
  permissionMode,
  onPermissionModeChange,
  workspaceTargetOn,
  onWorkspaceTargetChange,
  selectedMemberIds,
  onSelectedMemberIdsChange,
  members,
  currentUserId,
}: {
  permissionMode: AgentPermissionMode;
  onPermissionModeChange: (next: AgentPermissionMode) => void;
  workspaceTargetOn: boolean;
  onWorkspaceTargetChange: (next: boolean) => void;
  selectedMemberIds: Set<string>;
  onSelectedMemberIdsChange: (next: Set<string>) => void;
  members: MemberWithUser[];
  currentUserId: string | null;
}) {
  const { t } = useT("agents");
  const isPrivate = permissionMode === "private";
  const isWorkspace = !isPrivate && workspaceTargetOn;
  const isMembers = !isPrivate && !workspaceTargetOn;

  const otherMembers = members.filter((m) => m.user_id !== currentUserId);

  const toggleMember = (userId: string, checked: boolean) => {
    const next = new Set(selectedMemberIds);
    if (checked) next.add(userId);
    else next.delete(userId);
    onSelectedMemberIdsChange(next);
  };

  return (
    <div>
      <Label className="text-caption text-muted-foreground">
        {t(($) => $.create_dialog.access.label)}
      </Label>
      <fieldset className="mt-1.5 grid gap-2 sm:grid-cols-3">
        <legend className="sr-only">{t(($) => $.access.tooltip)}</legend>
        <CompactAccessChoice
          value="private"
          icon={Lock}
          title={t(($) => $.access.private_title)}
          description={t(($) => $.access.private_desc)}
          selected={isPrivate}
          onSelect={() => onPermissionModeChange("private")}
        />
        <CompactAccessChoice
          value="workspace"
          icon={Globe}
          title={t(($) => $.access.workspace_title)}
          description={t(($) => $.access.workspace_desc)}
          selected={isWorkspace}
          onSelect={() => {
            onPermissionModeChange("public_to");
            onWorkspaceTargetChange(true);
          }}
        />
        <CompactAccessChoice
          value="members"
          icon={Users}
          title={t(($) => $.access.members_title)}
          description={t(($) => $.access.members_desc)}
          selected={isMembers}
          onSelect={() => {
            onPermissionModeChange("public_to");
            onWorkspaceTargetChange(false);
          }}
        />
      </fieldset>

      {isMembers && (
        <div className="mt-2 rounded-lg border bg-muted/30 px-3 py-2">
          <div>
            {otherMembers.length === 0 ? (
              <div className="py-1 text-caption text-muted-foreground">
                {t(($) => $.create_dialog.access.public_members_empty)}
              </div>
            ) : (
              <div className="max-h-40 overflow-y-auto">
                {otherMembers.map((m) => {
                  const checked = selectedMemberIds.has(m.user_id);
                  return (
                    <label
                      key={m.user_id}
                      className="flex cursor-pointer items-center gap-2 rounded-md px-1 py-1 text-body hover:bg-background/60"
                    >
                      <Checkbox
                        checked={checked}
                        onCheckedChange={(v) =>
                          toggleMember(m.user_id, v === true)
                        }
                        aria-label={m.name}
                      />
                      <ActorAvatar
                        actorType="member"
                        actorId={m.user_id}
                        size="sm"
                      />
                      <span className="min-w-0 flex-1 truncate">{m.name}</span>
                    </label>
                  );
                })}
              </div>
            )}
          </div>

          {selectedMemberIds.size === 0 && (
            <div className="mt-2 text-caption text-destructive" role="alert">
              {t(($) => $.access.shared_target_required)}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** 三选一的紧凑卡片选项（radio + 图标 + 标题/描述）。 */
function CompactAccessChoice({
  value,
  icon: Icon,
  title,
  description,
  selected,
  onSelect,
}: {
  value: string;
  icon: typeof Lock;
  title: string;
  description: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <label
      className={`flex min-w-0 cursor-pointer items-start gap-2 rounded-lg border px-3 py-2.5 text-body transition-colors hover:bg-muted ${
        selected ? "border-primary bg-primary/5" : "border-border"
      }`}
    >
      <input
        type="radio"
        name="create-agent-access-scope"
        value={value}
        checked={selected}
        onChange={onSelect}
        className="mt-0.5 size-4 shrink-0 accent-foreground"
      />
      <Icon
        className="mt-0.5 size-4 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
      <span className="min-w-0">
        <span className="block font-medium">{title}</span>
        <span className="mt-0.5 block text-caption leading-4 text-muted-foreground">
          {description}
        </span>
      </span>
    </label>
  );
}
