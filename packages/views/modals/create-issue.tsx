// 本文件职责：创建 issue 弹窗的「手动模式」面板——标题、描述、状态/优先级等属性工具栏，以及最终的提交逻辑。
"use client";

import { issueStatusCategory } from "@multica/core/issues";
import { useState, useRef, useEffect, useLayoutEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { AppLink, resolveClickIntent, useNavigation } from "../navigation";
import {
  AlertTriangle,
  ArrowDown,
  ArrowLeftRight,
  ArrowUp,
  CalendarClock,
  CalendarDays,
  Check,
  ChevronRight,
  CircleUser,
  FolderKanban,
  Maximize2,
  Minimize2,
  MoreHorizontal,
  Settings2,
  Shapes,
  Tag,
  X as XIcon,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import type {
  Issue,
  IssueStatus,
  IssuePriority,
  IssueAssigneeType,
  IssuePropertyValue,
  SourceContextPreview,
} from "@multica/core/types";
import { contentReferencesAttachment } from "@multica/core/types";
import {
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@multica/ui/components/ui/tooltip";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { ContentEditor, type ContentEditorRef, TitleEditor, type TitleEditorRef, useFileDropZone, FileDropOverlay, useUploadGate, useComposerSubmit } from "../editor";
import { useIssueCreateUploads } from "./use-issue-create-uploads";
import { useShortcut } from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { StatusIcon, StatusPicker, PriorityIcon, PriorityPicker, StagePicker, AssigneePicker, StartDatePicker, DueDatePicker, LabelPicker } from "../issues/components";
import { maxSiblingStage } from "../issues/components/pickers/stage-picker";
import { ProjectPicker } from "../projects/components/project-picker";
import { useIssueTriggerPreview } from "../issues/hooks/use-issue-trigger-preview";
import { useActorName } from "@multica/core/workspace/hooks";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useIssueDraftStore, type IssueCreateDraft } from "@multica/core/issues/stores/draft-store";
import { useCreateModeStore } from "@multica/core/issues/stores/create-mode-store";
import { useQuickCreateStore } from "@multica/core/issues/stores/quick-create-store";
import {
  useIssueCreateSettingsStore,
  type ManualCreateField,
} from "@multica/core/issues/stores/issue-create-settings-store";
import { issueDetailOptions, childIssuesOptions } from "@multica/core/issues/queries";
import {
  useCreateCommentSubIssue,
  useCreateIssue,
  useUpdateIssue,
} from "@multica/core/issues/mutations";
import { useAttachLabelToIssue } from "@multica/core/labels";
import {
  propertyListOptions,
  useSetIssueProperty,
} from "@multica/core/properties";
import {
  ApiError,
  DuplicateIssueErrorBodySchema,
  type DuplicateIssueErrorBody,
  parseWithFallback,
} from "@multica/core/api";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { ClearablePillButton, PillButton } from "../common/pill-button";
import { ActorAvatar } from "../common/actor-avatar";
import { PropertyIcon } from "../common/property-icon";
import {
  CustomPropertyValueDisplay,
  CustomPropertyValueInput,
} from "../issues/components/pickers/custom-property-picker";
import { IssuePickerModal } from "./issue-picker-modal";
import { useT } from "../i18n";
import { SourceContextPreviewCard, useSourceContextFailureMessage } from "./source-context-preview";
import { useIssueLimitUpgradePrompt } from "./use-issue-limit-upgrade-prompt";

// ---------------------------------------------------------------------------
// ManualCreatePanel —— 创建 issue 弹窗的手动模式面板主体。渲染 DialogContent 及
// 其内部所有内容；外层 `<Dialog>` 由 CreateIssueDialog 持有，所以切换模式只替换
// 内部面板而不会重新挂载 Dialog 根节点（没有遮罩闪烁）。`onSwitchMode` 负责切换
// 外壳的本地模式状态。
// ---------------------------------------------------------------------------

// CreateRunHint 是创建弹窗里的被动「触发前提示」（MUL-3375 §4）：保存后是否会启动
// 一次运行，由统一的后端谓词（preview、isCreate）驱动——绝不靠前端猜测。没有
// 弹窗，也不阻塞。
//
// 视觉上借用评论区头部的「头像 + 文本」行，但去掉了交互性——纯粹是条说明文字，
// 绝不是链接或悬浮卡片。它自己渲染一条展开带（grid 0fr→1fr 折叠），独占属性工具栏
// 上方的一行而不引起任何回流：折叠时是 0px（flex-1 的编辑器吸收了这处高度差），
// 只有当谓词解析完成才展开，并直接动画到正确文案。
function CreateRunHint({
  assigneeType,
  assigneeId,
  status,
}: {
  assigneeType?: IssueAssigneeType;
  assigneeId?: string;
  status: IssueStatus;
}) {
  const { t } = useT("modals");
  const { getActorName } = useActorName();
  const isAgentLike = assigneeType === "agent" || assigneeType === "squad";
  const preview = useIssueTriggerPreview({
    isCreate: true,
    assigneeType: assigneeType ?? null,
    assigneeId: assigneeId ?? null,
    status,
    enabled: isAgentLike && !!assigneeId,
  });

  // 只在谓词解析完成后才展开，这样展开带能直接动画到最终文案，而不是先闪一下
  // 「已挂起」再等运行预览到位。
  const ready = isAgentLike && !!assigneeId && !preview.isLoading;
  const willStart = preview.totalCount > 0;
  const isSquad = assigneeType === "squad";
  const triggerAgentId = preview.triggers[0]?.agent_id ?? assigneeId;

  // 头像 + 文案跟着流程走。小队并不「干活」——是它的 leader 评估并分派——所以
  // 小队分支把小队当作主语（头像 + 名字），用「leader 会分派」的文案。单个 agent
  // 直接领取这个 issue；挂起的 issue 则显示它被指派给了谁。
  let avatarType: string;
  let avatarId: string | undefined;
  let text: string;
  if (!willStart) {
    avatarType = assigneeType ?? "agent";
    avatarId = assigneeId;
    text = t(($) => $.run_confirm.create_parked);
  } else if (isSquad) {
    avatarType = "squad";
    avatarId = assigneeId;
    text = t(($) => $.run_confirm.create_will_start_squad, {
      name: getActorName("squad", assigneeId ?? ""),
    });
  } else {
    avatarType = "agent";
    avatarId = triggerAgentId;
    text = t(($) => $.run_confirm.create_will_start, {
      name: getActorName("agent", triggerAgentId ?? assigneeId ?? ""),
    });
  }

  return (
    <div
      className={cn(
        "grid shrink-0 transition-[grid-template-rows] duration-200 ease-out motion-reduce:transition-none",
        ready ? "grid-rows-[1fr]" : "grid-rows-[0fr]",
      )}
      aria-hidden={!ready}
    >
      <div className="overflow-hidden">
        <div
          aria-live="polite"
          className="flex items-center gap-1.5 px-4 pb-1 pt-0.5 text-micro text-muted-foreground"
        >
          {avatarId && (
            <ActorAvatar
              actorType={avatarType}
              actorId={avatarId}
              size="sm"
              profileLink={false}
            />
          )}
          <span className="truncate">{text}</span>
        </div>
      </div>
    </div>
  );
}

/**
 * 创建 issue 的「手动模式」面板。
 *
 * 包含标题、描述编辑器、属性工具栏（状态/优先级/负责人/标签/项目等）以及提交逻辑，
 * 提交后创建 issue 并处理重复 issue、来源上下文变更、issue 上限等各类错误。
 *
 * @param onClose       关闭弹窗的回调
 * @param onSwitchMode  切换到 agent 模式时的回调，可携带预填数据
 * @param data          弹窗上下文，可携带预填字段、锚点评论、来源上下文预览等
 * @param isExpanded    面板是否处于展开（全屏）状态
 * @param setIsExpanded 设置展开状态
 */
export function ManualCreatePanel({
  onClose,
  onSwitchMode,
  data,
  isExpanded,
  setIsExpanded,
}: {
  onClose: () => void;
  /** 切换后携带的预填数据，用于给 agent 面板做种子。 */
  onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
  data?: Record<string, unknown> | null;
  /** 提升到外壳，让 DialogContent 的「模式感知」className 能响应它，而面板本体
   *  不必直接住在 DialogContent 里（否则切换模式会重新挂载 Portal，重放打开动画）。 */
  isExpanded: boolean;
  setIsExpanded: (v: boolean) => void;
}) {
  const { t } = useT("modals");
  const { t: tIssues } = useT("issues");
  const { t: tEditor } = useT("editor");
  const { t: tProjects } = useT("projects");
  const router = useNavigation();
  const p = useWorkspacePaths();
  const workspaceName = useCurrentWorkspace()?.name;
  const anchorCommentId = typeof data?.anchor_comment_id === "string" ? data.anchor_comment_id : null;
  const sourcePreview = data?.source_context_preview as SourceContextPreview | undefined;
  const sourceContextLoading = data?.source_context_loading === true;
  const sourceContextFailed = data?.source_context_failed === true;
  const sourceContextError = data?.source_context_error;
  const refetchSourceContext = data?.source_context_refetch as (() => Promise<unknown>) | undefined;
  const sourceContextExpanded = typeof data?.source_context_expanded === "boolean"
    ? data.source_context_expanded
    : undefined;
  const onSourceContextExpandedChange = data?.source_context_on_expanded_change as ((expanded: boolean) => void) | undefined;
  const sourceContextFailureMessage = useSourceContextFailureMessage();
  const showIssueLimitUpgradePrompt = useIssueLimitUpgradePrompt();

  const draft = useIssueDraftStore((s) => s.draft);
  const setManual = useIssueDraftStore((s) => s.setManual);
  const setShared = useIssueDraftStore((s) => s.setShared);
  const setAgent = useIssueDraftStore((s) => s.setAgent);
  const setActiveMode = useIssueDraftStore((s) => s.setActiveMode);
  const clearDraft = useIssueDraftStore((s) => s.clearDraft);
  const setLastAssignee = useIssueDraftStore((s) => s.setLastAssignee);
  const setLastMode = useCreateModeStore((s) => s.setLastMode);
  const keepOpen = useQuickCreateStore((s) => s.keepOpen);
  const setKeepOpen = useQuickCreateStore((s) => s.setKeepOpen);
  const manualFields = useIssueCreateSettingsStore((s) => s.manualCreateFields);

  const sendShortcut = useShortcut("send");
  const [title, setTitle] = useState(draft.manual.title);
  const [formResetKey, setFormResetKey] = useState(0);
  const titleEditorRef = useRef<TitleEditorRef>(null);
  const descEditorRef = useRef<ContentEditorRef>(null);
  const { isDragOver: descDragOver, dropZoneProps: descDropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => descEditorRef.current?.uploadFile(f)),
  });
  const [status, setStatus] = useState<IssueStatus>((data?.status as IssueStatus) || draft.manual.status);
  const [priority, setPriority] = useState<IssuePriority>(
    (data?.priority as IssuePriority | undefined) ?? draft.shared.priority,
  );
  const [assigneeType, setAssigneeType] = useState<IssueAssigneeType | undefined>(() => {
    if (data && "assignee_type" in data) {
      return (data.assignee_type as IssueAssigneeType | null) ?? undefined;
    }
    return draft.manual.assigneeType;
  });
  const [assigneeId, setAssigneeId] = useState<string | undefined>(() => {
    if (data && "assignee_id" in data) {
      return (data.assignee_id as string | null) ?? undefined;
    }
    return draft.manual.assigneeId;
  });
  const [startDate, setStartDate] = useState<string | null>(draft.manual.startDate);
  const [dueDate, setDueDate] = useState<string | null>(
    (data?.due_date as string | undefined) ?? draft.shared.dueDate,
  );
  const [labelIds, setLabelIds] = useState<string[]>(draft.manual.labelIds);
  const [propertyValues, setPropertyValues] = useState(draft.manual.propertyValues ?? {});
  const [customPropertyPickerId, setCustomPropertyPickerId] = useState<string | null>(null);
  const [projectId, setProjectId] = useState<string | undefined>(() => {
    if (data && "project_id" in data) {
      return (data.project_id as string | null) ?? undefined;
    }
    return draft.shared.projectId;
  });
  const [parentIssueId, setParentIssueId] = useState<string | undefined>(
    (data?.parent_issue_id as string) || undefined,
  );
  const parentIssueLocked = anchorCommentId !== null
    && typeof data?.parent_issue_id === "string"
    && data.parent_issue_id.length > 0;
  // Stage 只对子 issue 有意义；它留在本地（不进持久化草稿），因为它是与所选父
  // issue 绑定的一次性创建选择。
  const [stage, setStage] = useState<number | null>(
    typeof data?.stage === "number" ? (data.stage as number) : null,
  );
  const [parentPickerOpen, setParentPickerOpen] = useState(false);
  // 通过「设置 → Issue」隐藏的工具栏字段复用溢出展示模式：点击 ⋯ 菜单项把它置为
  // 打开，既挂载内联胶囊（作为 popover 的锚点），也打开选择器。没有取值就关闭时，
  // 胶囊再次卸载；持有非默认值的字段则无论设置如何始终渲染，保证已应用的值永不可见。
  const [fieldPickerOpen, setFieldPickerOpen] = useState<Exclude<
    ManualCreateField,
    "due_date" | "start_date"
  > | null>(null);
  // 开始日期是低频字段——默认待在 ⋯ 溢出菜单里。点击菜单项把它置为打开，既挂载
  // 内联胶囊（popover 的锚点），也打开日历。popover 没取值就关闭时，胶囊再次卸载。
  const [startDatePickerOpen, setStartDatePickerOpen] = useState(false);
  // 截止日期沿用和开始日期一样的溢出模式：默认折叠进 ⋯ 菜单，只有已经有值、或
  // 用户刚从菜单里打开它时，才以内联（作为 popover 锚点）的方式挂载。
  const [dueDatePickerOpen, setDueDatePickerOpen] = useState(false);
  // 子 issue 以完整 Issue 对象保存——选择器总是返回整个对象，我们不需要像父 issue
  // 那样再从 ID 去补全。
  const [childIssues, setChildIssues] = useState<Issue[]>([]);
  const [childPickerOpen, setChildPickerOpen] = useState(false);
  // 拉取父 issue 详情用于胶囊展示（状态/标识/标题）。列表缓存通常已有它，所以这里
  // 同步就能解析出来。
  const wsId = useWorkspaceId();
  const { categoryOf: draftStatusCategory } = useIssueStatuses(wsId);
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(wsId));
  const { data: parentIssue } = useQuery({
    ...issueDetailOptions(wsId, parentIssueId ?? ""),
    enabled: !!parentIssueId,
  });
  // 所选父 issue 下的同级 stage，让 Stage 选择器能给出「已用到的最大 stage（以及再
  // 下一级）」，而不是从 Stage 1–3 重新开始。
  const { data: parentChildren = [] } = useQuery({
    ...childIssuesOptions(wsId, parentIssueId ?? ""),
    enabled: !!parentIssueId,
  });

  // 把持久化草稿的 activeMode 设为 manual，这样之后重新打开（以及任何读取统一草稿
  // 的地方）都知道用户正在编辑哪个表单。
  useEffect(() => {
    setActiveMode("manual");
  }, [setActiveMode]);

  // 清理那些 markdown 引用在早前编辑会话里被删掉的已完成上传。只在挂载时跑一次：
  // 此时持久化的手动描述 / agent prompt 就是草稿正文（编辑器还没发生任何编辑），
  // 所以丢弃两边都没引用的 `uploaded` 记录是安全的。占位项（上传中/失败/中断）始终
  // 保留——它们还没有正文引用，状态胶囊是它们唯一的 UI。
  // 不在描述更新时清理——onUpdate 的 flush 可能和刚完成、markdown 链接还没来得及
  // 插入的上传赛跑，在那里清理会丢掉一个活跃的附件。
  useEffect(() => {
    const { draft: current } = useIssueDraftStore.getState();
    const uploads = current.shared.attachments ?? [];
    const kept = uploads.filter(
      (u) =>
        u.status !== "uploaded" ||
        contentReferencesAttachment(current.manual.description, u.attachment) ||
        contentReferencesAttachment(current.agent.prompt, u.attachment),
    );
    if (kept.length !== uploads.length) setShared({ attachments: kept });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 给所有「定稿」这个草稿的动作上闸：创建、以及切换到 agent 模式（切换时会从描述
  // 辅助初始化 agent prompt，可能把一个被截断的正文带过去）。
  const uploadGate = useUploadGate(descEditorRef);
  // 协调器持有的共享池上传（MUL-5181, L2）：在这里选的文件能跨弹窗关闭存活、退出
  // 登录时中止、刷新后被丢弃。`gate` 用共享池里的占位项扩宽了编辑器闸门。
  const {
    attachments: draftAttachments,
    handleUpload,
    gate,
  } = useIssueCreateUploads("manual", uploadGate, descEditorRef);

  // 把字段变化同步到草稿 store——仅手动模式的字段写手动槽位，项目/优先级/截止日期
  // 写共享槽位。
  const updateTitle = (v: string) => { setTitle(v); setManual({ title: v }); };
  const updateStatus = (v: IssueStatus) => { setStatus(v); setManual({ status: v }); };
  const updatePriority = (v: IssuePriority) => { setPriority(v); setShared({ priority: v }); };
  const updateAssignee = (type?: IssueAssigneeType, id?: string) => {
    setAssigneeType(type); setAssigneeId(id);
    setManual({ assigneeType: type, assigneeId: id });
  };
  const updateProject = (id?: string) => { setProjectId(id); setShared({ projectId: id }); };
  const updateStartDate = (v: string | null) => { setStartDate(v); setManual({ startDate: v }); };
  const updateDueDate = (v: string | null) => { setDueDate(v); setShared({ dueDate: v }); };
  const updateLabelIds = (ids: string[]) => { setLabelIds(ids); setManual({ labelIds: ids }); };
  const updatePropertyValue = (propertyId: string, value: IssuePropertyValue | undefined) => {
    const next = { ...propertyValues };
    if (value === undefined) delete next[propertyId];
    else next[propertyId] = value;
    setPropertyValues(next);
    setManual({ propertyValues: next });
  };

  // 每个工具栏字段的内联胶囊展示条件：「设置 → Issue」里保留了它；或持有非默认值
  // （有值的隐藏字段必须保持可见——草稿或模式切换携带的数据可能设过它）；或刚从 ⋯
  // 溢出菜单打开（选择器 popover 需要内联胶囊作锚点）。
  const showField = {
    status: manualFields.includes("status") || status !== "todo" || fieldPickerOpen === "status",
    priority: manualFields.includes("priority") || priority !== "none" || fieldPickerOpen === "priority",
    assignee: manualFields.includes("assignee") || assigneeId != null || fieldPickerOpen === "assignee",
    labels: manualFields.includes("labels") || labelIds.length > 0 || fieldPickerOpen === "labels",
    project: manualFields.includes("project") || projectId != null || fieldPickerOpen === "project",
    due_date: manualFields.includes("due_date") || dueDate !== null || dueDatePickerOpen,
    start_date: manualFields.includes("start_date") || startDate !== null || startDatePickerOpen,
  };

  const createIssueMutation = useCreateIssue();
  const createCommentSubIssueMutation = useCreateCommentSubIssue();
  const updateIssueMutation = useUpdateIssue();
  const attachLabelMutation = useAttachLabelToIssue();
  const setIssuePropertyMutation = useSetIssueProperty();
  const resetForNextIssue = () => {
    setTitle("");
    setStatus("todo");
    setPriority("none");
    setStartDate(null);
    setDueDate(null);
    setLabelIds([]);
    setPropertyValues({});
    setCustomPropertyPickerId(null);
    setProjectId(undefined);
    setParentIssueId(undefined);
    setStage(null);
    setChildIssues([]);
    // 保留刚用过的负责人给批次里的下一个 issue；其余全部重置（手动 + 共享两个槽位）。
    setManual({
      title: "",
      description: "",
      status: "todo",
      assigneeType,
      assigneeId,
      startDate: null,
      labelIds: [],
      propertyValues: {},
    });
    setShared({
      priority: "none",
      projectId: undefined,
      dueDate: null,
      attachments: [],
    });
    descEditorRef.current?.clearContent();
    setFormResetKey((key) => key + 1);
  };

  // 手动创建走共享的「先 await 后渲染」composer 契约（单飞 ref、提交时重查上传、
  // 加锁转圈、await→boolean、只在接受时清空）。手动模式以「标题」为门槛而不是编辑器
  // 正文——只有标题的 issue 也是合法的——所以 `normalize` 忽略描述 markdown，把标题
  // 当作空校验/内容传入；正文在 onSubmit 内部单独读取。
  // 过期提交守卫（MUL-5181 P0）：issue 草稿是一个单例 store，请求期间编辑器仍可交互。
  // 提交时快照草稿的对象身份；成功时只清空「未被改动」的草稿——无论改动发生在请求
  // 途中，还是来自重新打开的弹窗。
  const mountedRef = useRef(true);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const submittedDraftRef = useRef<IssueCreateDraft | null>(null);

  const composer = useComposerSubmit({
    editorRef: descEditorRef,
    uploadGate: gate,
    normalize: () => title.trim(),
    onSubmit: async (): Promise<boolean> => {
      // 在快照之前先把描述编辑器挂起的防抖更新 flush 进 store，这样「提交前的输入
      // 迟到的 flush」就不会被误判成「请求期间做的编辑」。
      const pendingDesc = descEditorRef.current?.flushPendingUpdate?.();
      if (pendingDesc != null) setManual({ description: pendingDesc });
      submittedDraftRef.current = useIssueDraftStore.getState().draft;
      try {
      const description = descEditorRef.current?.getMarkdown()?.trim() || undefined;
      const activeAttachmentIds = draftAttachments
        .filter((a) => contentReferencesAttachment(description ?? "", a))
        .map((a) => a.id);
      let issue: Issue;
      if (anchorCommentId && sourcePreview) {
        issue = await createCommentSubIssueMutation.mutateAsync({
          anchorCommentId,
          data: {
            mode: "manual",
            capture_token: sourcePreview.capture_token,
            issue: {
              title: title.trim(),
              description,
              status,
              priority,
              assignee_type: assigneeType,
              assignee_id: assigneeId,
              start_date: startDate || undefined,
              due_date: dueDate || undefined,
              attachment_ids: activeAttachmentIds.length > 0 ? activeAttachmentIds : undefined,
              label_ids: labelIds.length > 0 ? labelIds : undefined,
              stage: parentIssueId && stage != null ? stage : undefined,
              project_id: projectId,
            },
          },
        });
      } else {
        issue = await createIssueMutation.mutateAsync({
          title: title.trim(),
          description,
          status,
          priority,
          assignee_type: assigneeType,
          assignee_id: assigneeId,
          start_date: startDate || undefined,
          due_date: dueDate || undefined,
          attachment_ids: activeAttachmentIds.length > 0 ? activeAttachmentIds : undefined,
          // 服务端在与创建同一事务里附加这些标签并回显为 `issue.labels`，所以过期的
          // 选择会让创建失败，而不是留下一个「已提交但没标签」的 issue。早于这个能力
          // 的老后端会忽略该字段——由下面的兼容回退处理。
          label_ids: labelIds.length > 0 ? labelIds : undefined,
          parent_issue_id: parentIssueId,
          // Stage 只对子 issue 有意义（相对它的同级）。
          stage: parentIssueId && stage != null ? stage : undefined,
          project_id: projectId,
        });
      }

      // 自定义属性值只有在 issue 有了 id 之后才能写入。让弹窗保持提交中状态直到每个
      // 值都尘埃落定，避免「关闭」或「再创建一个」与这些扇出请求赛跑。
      const propertyEntries = Object.entries(propertyValues);
      if (propertyEntries.length > 0) {
        const results = await Promise.allSettled(
          propertyEntries.map(([propertyId, value]) =>
            setIssuePropertyMutation.mutateAsync({
              issueId: issue.id,
              propertyId,
              value,
            }),
          ),
        );
        let failed = 0;
        for (const result of results) {
          if (result.status === "rejected") {
            failed += 1;
            console.error("[create-issue] custom property set failed", result.reason);
          }
        }
        if (failed > 0) {
          toast.error(
            t(($) => $.create_issue.toast_set_properties_failed, { count: failed }),
          );
        }
      }

      // 把排队的子 issue 挂到新父 issue 下。推迟到创建之后执行，因为新 issue 的 ID
      // 那时还不存在。部分失败不会回滚新 issue——它已经提交了。
      if (childIssues.length > 0) {
        const results = await Promise.allSettled(
          childIssues.map((child) =>
            updateIssueMutation.mutateAsync({
              id: child.id,
              parent_issue_id: issue.id,
            }),
          ),
        );
        // 聚合扇出：N 个独立请求可能因 N 种不同原因失败。面向用户的 toast 保持基于
        // 数量（单独一条 err.message 会误导），但把每次拒绝都记到日志，让开发者仍能
        // 在 dev-tools / Sentry 里看到信号。
        for (const result of results) {
          if (result.status === "rejected") {
            console.error("[create-issue] sub-issue link failed", result.reason);
          }
        }
        const failed = results.filter((r) => r.status === "rejected").length;
        if (failed > 0) {
          toast.error(
            failed === childIssues.length
              ? t(($) => $.create_issue.toast_link_subissues_all_failed)
              : t(($) => $.create_issue.toast_link_subissues_partial, {
                  failed,
                  total: childIssues.length,
                }),
          );
        }
      }

      // 滚动部署窗口期的后端兼容回退：web 应用合并即自动部署，而后端手动部署，所以
      // 较新的 web 构建可能短暂地和一个「早于原子标签创建」的后端对话。那个后端会
      // 静默忽略 `label_ids`，返回一个没有 `labels` 字段的 issue。只有这时才回退到
      // 老的逐标签附加，以免用户的标签被静默丢弃。当 `labels` 存在（当前后端）时，
      // 原子路径已经跑过了，跳过这里——没有重复写入，也没有逐标签扇出。
      if (labelIds.length > 0 && issue.labels === undefined) {
        const results = await Promise.allSettled(
          labelIds.map((labelId) =>
            attachLabelMutation.mutateAsync({ issueId: issue.id, labelId }),
          ),
        );
        let labelsFailed = 0;
        for (const result of results) {
          if (result.status === "rejected") {
            labelsFailed += 1;
            console.error("[create-issue] label attach fallback failed", result.reason);
          }
        }
        if (labelsFailed > 0) {
          toast.error(t(($) => $.create_issue.toast_link_labels_failed));
        }
      }

      // 旧的创建后「agent 在 Backlog 里暂停」阻塞面板已移除——现在由一条被动内联
      // 提示在提交前警告（MUL-3375）。草稿重置 + 关闭/保持打开在报告成功后由
      // onAccepted 完成。
      {
        toast.custom((toastId) => (
          <div className="bg-popover text-popover-foreground border rounded-lg shadow-lg p-4 w-[360px]">
            <div className="flex items-center gap-2 mb-2">
              <div className="flex items-center justify-center size-5 rounded-full bg-emerald-500/15 text-emerald-500">
                <Check className="size-3" />
              </div>
              <span className="text-body font-medium">{t(($) => $.create_issue.toast_created)}</span>
            </div>
            <div className="flex items-center gap-2 text-body text-muted-foreground ml-7">
              <StatusIcon
                status={issue.status}
                category={issueStatusCategory(issue) ?? undefined}
                className="size-3.5 shrink-0"
              />
              <span className="truncate">{issue.identifier} – {issue.title}</span>
            </div>
            {/* 不是 AppLink：sonner 把 toast 内容渲染在 <Toaster /> 下，而它挂载在
                NavigationProvider 之外，这里用 useNavigation() 会抛错。 */}
            <button
              type="button"
              className="ml-7 mt-2 text-body text-primary hover:underline cursor-pointer"
              onClick={() => {
                router.push(p.issueDetail(issue.id));
                toast.dismiss(toastId);
              }}
            >
              {t(($) => $.create_issue.view_issue)}
            </button>
          </div>
        ), { duration: 5000 });
      }
      return true;
    } catch (err) {
      const sourceCode = err instanceof ApiError && err.body && typeof err.body === "object"
        ? (err.body as { code?: unknown }).code
        : undefined;
      if (anchorCommentId && (
        sourceCode === "source_context_changed"
        || sourceCode === "anchor_comment_deleted"
        || sourceCode === "source_issue_deleted"
      )) {
        await refetchSourceContext?.();
        toast.error(sourceContextFailureMessage(err) ?? tIssues(($) => $.source_context.error_source_changed));
        return false;
      }
      if (anchorCommentId && sourceCode === "source_context_server_unsupported") {
        toast.error(tIssues(($) => $.source_context.error_server_unsupported));
        return false;
      }
      if (anchorCommentId && sourceCode === "source_context_too_large") {
        toast.error(sourceContextFailureMessage(err) ?? tIssues(($) => $.source_context.error_too_large));
        return false;
      }
      if (sourceCode === "issue_limit_reached") {
        showIssueLimitUpgradePrompt();
        return false;
      }
      // 重复 issue 是创建端点唯一返回的结构化 409。我们用 schema 守卫 body
      // （ApiError.body 是 `unknown`），这样将来服务端重命名/删除 `code`/`issue`
      // 时会退化成普通错误 toast，而不是在 toast 渲染器里抛异常。
      if (err instanceof ApiError && err.status === 409) {
        const dup = parseWithFallback<DuplicateIssueErrorBody | null>(
          err.body,
          DuplicateIssueErrorBodySchema,
          null,
          { endpoint: "POST /api/workspaces/:wsId/issues (active_duplicate_issue)" },
        );
        if (dup) {
          toast.custom(
            (toastId) => (
              <div className="bg-popover text-popover-foreground border rounded-lg shadow-lg p-4 w-[360px]">
                <div className="flex items-center gap-2 mb-2">
                  <div className="flex items-center justify-center size-5 rounded-full bg-amber-500/15 text-amber-500">
                    <AlertTriangle className="size-3" />
                  </div>
                  <span className="text-body font-medium">
                    {t(($) => $.create_issue.toast_duplicate_title)}
                  </span>
                </div>
                <div className="flex items-center gap-2 text-body text-muted-foreground ml-7">
                  <span className="truncate">{dup.issue.identifier} – {dup.issue.title}</span>
                </div>
                {/* 见上方「创建成功」toast：toast 内容住在 NavigationProvider 之外，
                    所以这里只能是 button。 */}
                <button
                  type="button"
                  className="ml-7 mt-2 text-body text-primary hover:underline cursor-pointer"
                  onClick={() => {
                    router.push(p.issueDetail(dup.issue.id));
                    toast.dismiss(toastId);
                  }}
                >
                  {t(($) => $.create_issue.toast_duplicate_view)}
                </button>
              </div>
            ),
            { duration: 5000 },
          );
          return false;
        }
      }
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.create_issue.toast_failed),
      );
      return false;
    }
  },
    onAccepted: () => {
      // 这些偏好取自「提交时」的值，而不是实时草稿——issue 已经创建了，所以无论守卫
      // 如何都要记录。
      setLastAssignee(assigneeType, assigneeId);
      setLastMode("manual");
      // 成功只能消费它提交的那份草稿（MUL-5181 P0）：提交快照之后的任何编辑——请求
      // 在途时的输入，或重新打开的弹窗——都保留下来，弹窗就停在较新的草稿上，而不是
      // 在它之上关闭/重置。先 flush 编辑器挂起的防抖，让仍在防抖窗口内的在途输入被
      // 正确判定。
      const lateDesc = descEditorRef.current?.flushPendingUpdate?.();
      if (lateDesc != null) setManual({ description: lateDesc });
      const untouched =
        useIssueDraftStore.getState().draft === submittedDraftRef.current;
      if (untouched) clearDraft();
      if (!mountedRef.current || !untouched) return;
      if (keepOpen) {
        resetForNextIssue();
      } else {
        onClose();
      }
    },
  });

  // 按钮 + 快捷键的统一入口。标题为空的情况不能依赖按钮 tooltip（快捷键会绕过按钮），
  // 所以直接聚焦标题框指出问题所在；否则交给 composer（单飞 + 闸门都在那里）。
  const handleSubmit = () => {
    if (anchorCommentId && !sourcePreview) return;
    if (!title.trim()) {
      titleEditorRef.current?.focus();
      return;
    }
    void composer.submit();
  };
  const submitting = composer.submitting;

  // 切换到 agent 模式，但不销毁手动草稿。手动槽位（标题、描述……）原样保留，这样之后
  // agent→manual 切回来能逐字恢复。项目/优先级/截止日期本来就在共享槽位里，免费跟着
  // 一起过去。只有两样东西交给 agent 面板：
  //   1. 一次性的 agent prompt / actor 辅助初始化：当 agent 草稿还是空的时候，用
  //      标题 + 描述做 prompt 种子，用手动负责人（如果像 agent）做 actor 种子。已有
  //      的 agent 草稿则保留——不做反复「拼接再覆盖」。
  //   2. 父 issue 上下文，它不持久化在草稿里（它是「添加子 issue」这个入口的一次性
  //      意图），所以走携带通道。
  const switchToAgent = () => {
    // 上传中途序列化，会把一份已经丢掉待传图片的描述塞进 agent prompt，所以切换也要
    // 上闸。
    if (gate.isBlocked()) return;
    // 把共享字段写进草稿，让 agent 面板从那里读。本地 state 可能持有从 `data` 做种子
    // 的值（比如打开者的项目）而从未经过选择器写出，直接翻转会丢掉它。
    setShared({ projectId, priority, dueDate });
    const existingPrompt = draft.agent.prompt;
    if (!existingPrompt.trim()) {
      const desc = descEditorRef.current?.getMarkdown()?.trim() ?? "";
      const seeded = [title.trim(), desc].filter(Boolean).join("\n\n");
      if (seeded) setAgent({ prompt: seeded });
    }
    if (
      !draft.agent.actorId &&
      assigneeId &&
      (assigneeType === "agent" || assigneeType === "squad")
    ) {
      setAgent({ actorType: assigneeType, actorId: assigneeId });
    }
    setLastMode("agent");
    setActiveMode("agent");
    // 优先用 `parentIssue` 补全出来的标识，回退到弹窗打开者种在 `data` 上的标识。
    // 没有这个回退，若在 issue 详情查询解析前就翻转，标识会丢掉，agent 胶囊会渲染成
    // 尾巴为空的「是 xxx 的子 issue」。仅 UUID 仍然能正确关联子 issue 关系；这里只
    // 影响展示。
    const carryParentIdentifier =
      parentIssue?.identifier ?? (data?.parent_issue_identifier as string | undefined);
    const carry: Record<string, unknown> = {};
    if (parentIssueId) carry.parent_issue_id = parentIssueId;
    if (carryParentIdentifier) carry.parent_issue_identifier = carryParentIdentifier;
    onSwitchMode?.(Object.keys(carry).length > 0 ? carry : null);
  };

  // 按钮和键盘路径共用一份状态，这样渲染出来的外观永远不会和 `handleSubmit` 实际
  // 会做的事不一致。
  const submitState: "submitting" | "uploading" | "missing_title" | "source_unavailable" | "ready" =
    submitting
      ? "submitting"
      : gate.uploading
        ? "uploading"
        : anchorCommentId && !sourcePreview
          ? "source_unavailable"
          : !title.trim()
            ? "missing_title"
            : "ready";
  const submitBusy = submitState === "submitting" || submitState === "uploading";

  // 只构建一次，两个 footer 分支共用：之前每个分支各渲染一个 Button，导致快捷键
  // 键帽在其中一个分支里漂移丢失。
  const createButton = (
    <Button
      size="sm"
      onClick={handleSubmit}
      // 瞬时的忙碌状态用原生 `disabled`，但「标题缺失」用 `aria-disabled`——原生
      // disabled 的按钮不可聚焦，键盘和读屏用户就永远够不到那个解释「为什么没反应」的
      // tooltip。无论哪种，`handleSubmit` 都是真正的门槛。
      disabled={submitBusy}
      aria-disabled={submitState === "missing_title" || submitState === "source_unavailable" || undefined}
      aria-busy={submitBusy || undefined}
      // Button 基类只在原生 `disabled` 时变暗/拦截，所以仅 aria-disabled 时它仍是一个
      // 完全点亮、看似可点的主按钮。故意不加 `pointer-events-none`：这个控件仍需
      // hover 出自己的 tooltip，并接收点击去聚焦标题。
      className="justify-self-end aria-disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:active:translate-y-0"
    >
      {submitState === "submitting" ? (
        t(($) => $.create_issue.submitting)
      ) : submitState === "uploading" ? (
        tEditor(($) => $.upload.in_progress)
      ) : (
        <>
          {t(($) => $.create_issue.submit)}
          {/* 纯装饰：无障碍名称必须保持「创建 Issue」，而不是「创建 Issue Command
              Enter」。`send` 未绑定时不显示。手机上隐藏——那里没有 ⌘ 键，且 footer
              行此时最紧凑。 */}
          {sendShortcut ? (
            <ShortcutKeycaps
              shortcut={sendShortcut}
              decorative
              className="ml-1 max-sm:hidden"
              keyClassName="border-background/30 bg-background/15 text-primary-foreground shadow-none"
            />
          ) : null}
        </>
      )}
    </Button>
  );

  return (
    <>
            <DialogTitle className="sr-only">{t(($) => $.create_issue.sr_manual)}</DialogTitle>

            {/* 弹窗头部——工作区面包屑 + 展开/收起与关闭按钮 */}
            <div className="flex items-center justify-between px-5 pt-3 pb-2 shrink-0">
              <div className="flex items-center gap-1.5 text-caption">
                <span className="text-muted-foreground">{workspaceName}</span>
                <ChevronRight className="size-3 text-faint-foreground" />
                <span className="font-medium">{t(($) => $.create_issue.manual_breadcrumb)}</span>
              </div>
              <div className="flex items-center gap-1">
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <button
                        type="button"
                        onClick={() => setIsExpanded(!isExpanded)}
                        className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
                      >
                        {isExpanded ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
                      </button>
                    }
                  />
                  <TooltipContent side="bottom">
                    {isExpanded
                      ? t(($) => $.common.collapse_tooltip)
                      : t(($) => $.common.expand_tooltip)}
                  </TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <button
                        type="button"
                        onClick={onClose}
                        className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
                      >
                        <XIcon className="size-4" />
                      </button>
                    }
                  />
                  <TooltipContent side="bottom">{t(($) => $.common.close)}</TooltipContent>
                </Tooltip>
              </div>
            </div>

            {/* 标题输入 */}
            <div className="px-5 pb-2 shrink-0">
              <TitleEditor
                key={formResetKey}
                ref={titleEditorRef}
                autoFocus
                defaultValue={draft.manual.title}
                placeholder={t(($) => $.create_issue.title_placeholder)}
                className="text-title font-semibold"
                onChange={(v) => updateTitle(v)}
                // 只响应组合键——普通 Enter 仍然只是结束标题编辑（#5532）。
                onSubmitShortcut={handleSubmit}
              />
            </div>

            {/* 描述——占据剩余空间 */}
            <div {...descDropZoneProps} className="relative flex flex-1 min-h-0 overflow-y-auto px-5">
              <ContentEditor
                ref={descEditorRef}
                defaultValue={draft.manual.description}
                placeholder={t(($) => $.create_issue.description_placeholder)}
                onUpdate={(md) => setManual({ description: md })}
                onSubmit={handleSubmit}
                onUploadFile={handleUpload}
                onUploadingChange={uploadGate.onUploadingChange}
                debounceMs={500}
                attachments={draftAttachments}
              />
              {descDragOver && <FileDropOverlay />}
            </div>

            {anchorCommentId && (
              <SourceContextPreviewCard
                preview={sourcePreview}
                loading={sourceContextLoading}
                failed={sourceContextFailed}
                error={sourceContextError}
                onRetry={refetchSourceContext ? () => { void refetchSourceContext(); } : undefined}
                constrainToParent
                expanded={sourceContextExpanded}
                onExpandedChange={onSourceContextExpandedChange}
              />
            )}

            {/* 触发前预览——工具栏上方的一条被动说明；当 agent 负责人会领取这个 issue
                时显示。 */}
            <CreateRunHint assigneeType={assigneeType} assigneeId={assigneeId} status={status} />

            {/* 属性工具栏——每个字段按「设置 → Issue」的选择渲染（见上方 showField）。 */}
            <div className="flex items-center gap-1.5 px-4 py-2 shrink-0 flex-wrap">
              {/* 状态 */}
              {showField.status && (
                <StatusPicker
                  status={status}
                  onUpdate={(u) => { if (u.status) updateStatus(u.status); }}
                  triggerRender={<PillButton />}
                  align="start"
                  open={fieldPickerOpen === "status" ? true : undefined}
                  onOpenChange={(open) => setFieldPickerOpen(open ? "status" : null)}
                />
              )}

              {/* 优先级 */}
              {showField.priority && (
                <PriorityPicker
                  priority={priority}
                  onUpdate={(u) => { if (u.priority) updatePriority(u.priority); }}
                  triggerRender={<PillButton />}
                  align="start"
                  open={fieldPickerOpen === "priority" ? true : undefined}
                  onOpenChange={(open) => setFieldPickerOpen(open ? "priority" : null)}
                />
              )}

              {/* 负责人 */}
              {showField.assignee && (
                <AssigneePicker
                  assigneeType={assigneeType ?? null}
                  assigneeId={assigneeId ?? null}
                  onUpdate={(u) => updateAssignee(
                    u.assignee_type ?? undefined,
                    u.assignee_id ?? undefined,
                  )}
                  triggerRender={<PillButton />}
                  align="start"
                  open={fieldPickerOpen === "assignee" ? true : undefined}
                  onOpenChange={(open) => setFieldPickerOpen(open ? "assignee" : null)}
                />
              )}

              {/* 标签——占据原来放「截止日期」的位置，让「加标签」入口直接暴露在弹窗上。
                  草稿模式：选中在本地，直到 issue 创建后才由 handleSubmit 附加标签。 */}
              {showField.labels && (
                <LabelPicker
                  selectedIds={labelIds}
                  onSelectedIdsChange={updateLabelIds}
                  triggerRender={<PillButton />}
                  align="start"
                  open={fieldPickerOpen === "labels" ? true : undefined}
                  onOpenChange={(open) => setFieldPickerOpen(open ? "labels" : null)}
                />
              )}

              {/* 项目 */}
              {showField.project && (
                <ProjectPicker
                  projectId={projectId ?? null}
                  onUpdate={(u) => updateProject(u.project_id ?? undefined)}
                  triggerRender={
                    <ClearablePillButton
                      onClear={projectId ? () => updateProject(undefined) : undefined}
                      clearLabel={tProjects(($) => $.picker.clear_aria)}
                    />
                  }
                  align="start"
                  open={fieldPickerOpen === "project" ? true : undefined}
                  onOpenChange={(open) => setFieldPickerOpen(open ? "project" : null)}
                />
              )}

              {/* 阶段——只在父 issue 下创建子 issue 时才相关 */}
              {parentIssueId && (
                <StagePicker
                  stage={stage}
                  onUpdate={(u) => setStage(u.stage ?? null)}
                  maxStage={maxSiblingStage(parentChildren)}
                  triggerRender={<PillButton />}
                  align="start"
                />
              )}

              {/* 开始日期——默认折叠进 ⋯ 菜单，因为它是低频字段（可通过「设置 → Issue」
                  暴露）。当配置为可见、字段已有值、或用户刚从溢出菜单打开它时（选择器的
                  日历 popover 需要内联胶囊作锚点）才内联渲染。 */}
              {showField.start_date && (
                <StartDatePicker
                  startDate={startDate}
                  onUpdate={(u) => updateStartDate(u.start_date ?? null)}
                  triggerRender={<PillButton />}
                  align="start"
                  open={startDatePickerOpen}
                  onOpenChange={setStartDatePickerOpen}
                />
              )}

              {/* 截止日期——默认折叠进 ⋯ 菜单（从工具栏移开，给标签腾位置）。展示规则
                  与开始日期相同。 */}
              {showField.due_date && (
                <DueDatePicker
                  dueDate={dueDate}
                  onUpdate={(u) => updateDueDate(u.due_date ?? null)}
                  triggerRender={<PillButton />}
                  align="start"
                  open={dueDatePickerOpen}
                  onOpenChange={setDueDatePickerOpen}
                />
              )}

              {/* 工作区自定义字段使用与 issue 详情相同的类型化编辑器，但在创建之前写进
                  持久化草稿。 */}
              {workspaceProperties
                .filter(
                  (property) =>
                    Object.prototype.hasOwnProperty.call(propertyValues, property.id) ||
                    customPropertyPickerId === property.id,
                )
                .map((property) => {
                  const value = propertyValues[property.id];
                  return (
                    <CustomPropertyValueInput
                      key={property.id}
                      property={property}
                      value={value}
                      onChange={(next) => updatePropertyValue(property.id, next)}
                      open={customPropertyPickerId === property.id}
                      onOpenChange={(open) =>
                        setCustomPropertyPickerId(open ? property.id : null)
                      }
                      triggerRender={<PillButton />}
                      trigger={
                        <>
                          <PropertyIcon property={property} className="size-3.5 text-caption" />
                          <span className="max-w-32 truncate">{property.name}</span>
                          {value !== undefined && (
                            <span className="max-w-40 truncate text-muted-foreground">
                              <CustomPropertyValueDisplay property={property} value={value} />
                            </span>
                          )}
                        </>
                      }
                    />
                  );
                })}

              {/* 父 issue 胶囊——设置父 issue 时出现。放在 ⋯ 之前，这样空间紧张时会和
                  ⋯ 一起换到新行，但 ⋯ 在 DOM 顺序里永远排在最后。 */}
              {parentIssueId && parentIssueLocked ? (
                <span
                  data-testid="manual-sub-issue-chip"
                  className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-caption text-muted-foreground"
                >
                  {t(($) => $.create_issue.subissue_of, {
                    identifier: parentIssue?.identifier
                      ?? (data?.parent_issue_identifier as string | undefined)
                      ?? "",
                  })}
                </span>
              ) : parentIssueId && parentIssue ? (
                <div className="inline-flex items-center rounded-full border text-caption transition-colors hover:bg-accent/60">
                  <button
                    type="button"
                    onClick={() => setParentPickerOpen(true)}
                    className="flex items-center gap-1.5 py-1 pl-2.5 cursor-pointer"
                  >
                    <ArrowUp className="size-3 text-muted-foreground" />
                    <span>
                      {t(($) => $.create_issue.subissue_of, { identifier: parentIssue.identifier })}
                    </span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setParentIssueId(undefined)}
                    className="p-1 pr-2 text-muted-foreground hover:text-foreground cursor-pointer"
                    aria-label={t(($) => $.create_issue.remove_parent_aria)}
                  >
                    <XIcon className="size-3" />
                  </button>
                </div>
              ) : null}

              {/* 子 issue 胶囊——每个排队的子 issue 一个。关联推迟到创建解析后（见
                  handleSubmit）。 */}
              {childIssues.map((c) => (
                <div
                  key={c.id}
                  className="inline-flex items-center rounded-full border text-caption transition-colors hover:bg-accent/60"
                >
                  <div className="flex items-center gap-1.5 py-1 pl-2.5">
                    <ArrowDown className="size-3 text-muted-foreground" />
                    <span>{t(($) => $.create_issue.subissue_chip, { identifier: c.identifier })}</span>
                  </div>
                  <button
                    type="button"
                    onClick={() =>
                      setChildIssues((prev) => prev.filter((x) => x.id !== c.id))
                    }
                    className="p-1 pr-2 text-muted-foreground hover:text-foreground cursor-pointer"
                    aria-label={t(($) => $.create_issue.remove_subissue_aria, { identifier: c.identifier })}
                  >
                    <XIcon className="size-3" />
                  </button>
                </div>
              ))}

              {/* 溢出菜单——永远是最后一个子元素，让 DOM 顺序把它固定在换行流的末尾，
                  无论有多少胶囊。 */}
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <PillButton aria-label={t(($) => $.create_issue.more_options_aria)}>
                      <MoreHorizontal className="size-3.5" />
                    </PillButton>
                  }
                />
                <DropdownMenuContent align="start" className="w-auto">
                  {/* 被「设置 → Issue」隐藏的工具栏字段在这里提供重新入口。按工具栏
                      顺序排列；每一项都以「内联」方式打开选择器（同时挂载胶囊作为
                      popover 锚点）。 */}
                  {!showField.status && (
                    <DropdownMenuItem onClick={() => setFieldPickerOpen("status")}>
                      <StatusIcon
                        status={status}
                        category={draftStatusCategory(status)}
                        className="h-3.5 w-3.5"
                      />
                      {t(($) => $.create_issue.set_status)}
                    </DropdownMenuItem>
                  )}
                  {!showField.priority && (
                    <DropdownMenuItem onClick={() => setFieldPickerOpen("priority")}>
                      <PriorityIcon priority="none" className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_priority)}
                    </DropdownMenuItem>
                  )}
                  {!showField.assignee && (
                    <DropdownMenuItem onClick={() => setFieldPickerOpen("assignee")}>
                      <CircleUser className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_assignee)}
                    </DropdownMenuItem>
                  )}
                  {!showField.labels && (
                    <DropdownMenuItem onClick={() => setFieldPickerOpen("labels")}>
                      <Tag className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_labels)}
                    </DropdownMenuItem>
                  )}
                  {!showField.project && (
                    <DropdownMenuItem onClick={() => setFieldPickerOpen("project")}>
                      <FolderKanban className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_project)}
                    </DropdownMenuItem>
                  )}
                  {!showField.due_date && (
                    <DropdownMenuItem onClick={() => setDueDatePickerOpen(true)}>
                      <CalendarDays className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_due_date)}
                    </DropdownMenuItem>
                  )}
                  {!showField.start_date && (
                    <DropdownMenuItem onClick={() => setStartDatePickerOpen(true)}>
                      <CalendarClock className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_start_date)}
                    </DropdownMenuItem>
                  )}
                  {!parentIssueLocked && (parentIssueId && parentIssue ? (
                    <DropdownMenuItem onClick={() => setParentPickerOpen(true)}>
                      <ArrowUp className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.parent_with_id, { identifier: parentIssue.identifier })}
                    </DropdownMenuItem>
                  ) : (
                    <DropdownMenuItem onClick={() => setParentPickerOpen(true)}>
                      <ArrowUp className="h-3.5 w-3.5" />
                      {t(($) => $.create_issue.set_parent)}
                    </DropdownMenuItem>
                  ))}
                  <DropdownMenuItem onClick={() => setChildPickerOpen(true)}>
                    <ArrowDown className="h-3.5 w-3.5" />
                    {t(($) => $.create_issue.add_subissue)}
                  </DropdownMenuItem>
                  {workspaceProperties.length > 0 && (
                    <DropdownMenuSub>
                      <DropdownMenuSubTrigger>
                        <Shapes className="h-3.5 w-3.5" />
                        {t(($) => $.create_issue.custom_properties)}
                      </DropdownMenuSubTrigger>
                      <DropdownMenuSubContent className="w-56">
                        {workspaceProperties.map((property) => (
                          <DropdownMenuItem
                            key={property.id}
                            disabled={Object.prototype.hasOwnProperty.call(
                              propertyValues,
                              property.id,
                            )}
                            onClick={() => setCustomPropertyPickerId(property.id)}
                          >
                            <PropertyIcon property={property} className="size-3.5 text-caption" />
                            <span className="truncate">{property.name}</span>
                            {Object.prototype.hasOwnProperty.call(
                              propertyValues,
                              property.id,
                            ) && <Check className="ml-auto size-3.5" />}
                          </DropdownMenuItem>
                        ))}
                      </DropdownMenuSubContent>
                    </DropdownMenuSub>
                  )}
                  <DropdownMenuSeparator />
                  {/* 字段可见性存放在「设置 → Issue」；导航到设置页前先把本弹窗关掉，
                      免得对话框叠在设置页上。用户已输入的内容都持久化在草稿 store 里，
                      来回一趟不会丢。 */}
                  <DropdownMenuItem
                    render={
                      <AppLink
                        href={`${p.settings()}?tab=issue`}
                        onClick={(e) => {
                          // 带修饰键的点击会在另一个标签页打开设置——此时弹窗（连同其中
                          // 的草稿）保持原样。只有「当前页内跳转」才需要先关闭弹窗。
                          if (resolveClickIntent(e) !== "push") return;
                          onClose();
                        }}
                      />
                    }
                  >
                    <Settings2 className="h-3.5 w-3.5" />
                    {t(($) => $.create_issue.customize_fields)}
                  </DropdownMenuItem>
                  {!parentIssueLocked && parentIssueId && parentIssue && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        variant="destructive"
                        onClick={() => setParentIssueId(undefined)}
                      >
                        <XIcon className="h-3.5 w-3.5" />
                        {t(($) => $.create_issue.remove_parent)}
                      </DropdownMenuItem>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>

            {/* 父/子 issue 选择器——以「内联」方式渲染，这样它们会叠在本弹窗之上，
                而不是通过 useModalStore 把本弹窗替换掉。 */}
            <IssuePickerModal
              open={parentPickerOpen}
              onOpenChange={setParentPickerOpen}
              title={t(($) => $.create_issue.set_parent_picker.title)}
              description={t(($) => $.create_issue.set_parent_picker.description)}
              excludeIds={[
                ...childIssues.map((c) => c.id),
                ...(parentIssueId ? [parentIssueId] : []),
              ]}
              onSelect={(selected) => {
                setParentIssueId(selected.id);
              }}
            />
            <IssuePickerModal
              open={childPickerOpen}
              onOpenChange={setChildPickerOpen}
              title={t(($) => $.create_issue.add_subissue_picker.title)}
              description={t(($) => $.create_issue.add_subissue_picker.description)}
              excludeIds={[
                ...childIssues.map((c) => c.id),
                ...(parentIssueId ? [parentIssueId] : []),
              ]}
              onSelect={(selected) => {
                setChildIssues((prev) =>
                  prev.some((x) => x.id === selected.id) ? prev : [...prev, selected],
                );
              }}
            />

            {/* Footer——手机端同为 2×2 网格、`sm` 起恢复单行，与 agent 面板一致；
                原因见 AgentCreatePanel footer 的说明（MUL-6236）。TooltipProvider/
                Tooltip 不渲染 DOM，TooltipContent 又走 portal，所以下面两个分支里
                Create 按钮始终是 grid 的直接子元素。 */}
            <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-2.5 border-t px-4 py-3 shrink-0 sm:flex sm:flex-wrap">
              <div className="flex min-h-7 items-center gap-2 sm:mr-auto">
                <FileUploadButton
                  size="sm"
                  multiple
                  onSelect={(file) => descEditorRef.current?.uploadFile(file)}
                />
              </div>
              <button
                type="button"
                onClick={switchToAgent}
                disabled={gate.uploading}
                aria-disabled={gate.uploading || undefined}
                aria-busy={gate.uploading || undefined}
                title={t(($) => $.create_issue.switch_to_agent_tooltip)}
                className="border-beam group flex shrink-0 items-center gap-1.5 justify-self-end text-caption px-2 py-1 rounded-sm text-muted-foreground bg-brand/5 hover:bg-brand/10 hover:text-foreground transition-colors cursor-pointer disabled:cursor-not-allowed disabled:opacity-50"
              >
                <ArrowLeftRight className="size-3.5 text-brand transition-transform duration-300 group-hover:rotate-180" />
                {t(($) => $.create_issue.switch_to_agent)}
              </button>
              <label className="flex shrink-0 items-center gap-1.5 text-caption text-muted-foreground cursor-pointer select-none">
                <Switch
                  size="sm"
                  checked={keepOpen}
                  onCheckedChange={setKeepOpen}
                />
                {t(($) => $.create_issue.create_another)}
              </label>
              {submitState === "missing_title" ? (
                <TooltipProvider delay={200}>
                  <Tooltip>
                    {/* 现在不需要 `<span>` 包裹了：aria-disabled 让按钮依然可聚焦、
                        可 hover，因此它能直接作为自己 tooltip 的锚点。 */}
                    <TooltipTrigger render={createButton} />
                    <TooltipContent side="top">{t(($) => $.create_issue.title_required)}</TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              ) : (
                createButton
              )}
            </div>
    </>
  );
}

/**
 * 手动模式下 DialogContent 的 className——取决于是否展开（isExpanded）。
 * 导出它，是为了让拥有 DialogContent 的外壳（create-issue-dialog）复用同一套
 * 视觉样式，而不必重复写一遍。
 */
export function manualDialogContentClass(isExpanded: boolean) {
  return cn(
    "p-0 gap-0 flex flex-col overflow-hidden",
    "!top-1/2 !left-1/2 !-translate-x-1/2",
    "!transition-all !duration-300 !ease-out",
    // 手机端留边——见 create-issue-dialog.tsx 里对应的说明：下面这些 `!important`
    // 宽度同时覆盖了 DialogContent 自带的 `max-w-[calc(100%-2rem)]`，会让卡片在
    // 手机上贴满屏幕两侧（MUL-6236）。`!h-96` 保持为固定高度：它已能容纳我们所
    // 支持的最矮手机。
    "!w-full !max-w-[calc(100vw-1.5rem)]",
    isExpanded
      ? "!h-5/6 !-translate-y-1/2 sm:!max-w-4xl"
      : "!h-96 !-translate-y-1/2 sm:!max-w-2xl",
  );
}

// 一个薄薄的「包 Dialog 的」导出：注册表会把面板直接挂到外壳共享的 Dialog 下，
// 但少数历史调用方（以及测试套件）仍 import 本模块的弹窗版。独立使用时，运行行为
// 与重构前的组件一致。
import { Dialog as DialogRoot } from "@multica/ui/components/ui/dialog";
/**
 * 「创建 issue」完整弹窗（自包含版本，供旧调用方/测试直接使用）。
 *
 * 自己持有 Dialog 与 DialogContent，不依赖 ModalRegistry 提供的外壳；
 * 内部即渲染手动面板。
 */
export function CreateIssueModal(props: {
  onClose: () => void;
  data?: Record<string, unknown> | null;
}) {
  const [isExpanded, setIsExpanded] = useState(false);
  return (
    <DialogRoot open onOpenChange={(v) => { if (!v) props.onClose(); }}>
      <DialogContent
        finalFocus={false}
        showCloseButton={false}
        className={manualDialogContentClass(isExpanded)}
      >
        <ManualCreatePanel
          {...props}
          isExpanded={isExpanded}
          setIsExpanded={setIsExpanded}
        />
      </DialogContent>
    </DialogRoot>
  );
}
