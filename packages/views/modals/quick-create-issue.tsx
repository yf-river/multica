// 本文件职责：创建 issue 弹窗的「Agent 模式」面板（Quick Create）——选定 agent/小队、
// 写 prompt、可选项目/优先级/截止日期与图片，提交后由所选 agent 异步执行并产出 issue。
"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowLeftRight,
  CalendarDays,
  Check,
  ChevronRight,
  FolderKanban,
  Maximize2,
  Minimize2,
  MoreHorizontal,
  Settings2,
  X as XIcon,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { DialogTitle } from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { AppLink, resolveClickIntent } from "../navigation";
import { agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import {
  useQuickCreateStore,
  type QuickCreateActorType,
} from "@multica/core/issues/stores/quick-create-store";
import {
  useIssueCreateSettingsStore,
  type QuickCreateField,
} from "@multica/core/issues/stores/issue-create-settings-store";
import { useIssueDraftStore, type IssueCreateDraft } from "@multica/core/issues/stores/draft-store";
import { useCreateModeStore } from "@multica/core/issues/stores/create-mode-store";
import {
  runtimeListOptions,
  checkQuickCreateCliVersion,
  checkQuickCreateFieldsCliVersion,
  readRuntimeCliVersion,
} from "@multica/core/runtimes";
import { useShortcut } from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import {
  contentReferencesAttachment,
  type Agent,
  type IssuePriority,
  type SourceContextPreview,
  type Squad,
} from "@multica/core/types";
import { ActorAvatar } from "../common/actor-avatar";
import { ClearablePillButton, PillButton } from "../common/pill-button";
import { ProjectPicker } from "../projects/components/project-picker";
import { DueDatePicker, PriorityIcon, PriorityPicker } from "../issues/components";
import { canAssignAgent } from "../issues/components/pickers/assignee-picker";
import { isAgentRuntimeBound } from "@multica/core/agents";
import {
  PropertyPicker,
  PickerItem,
  PickerSection,
  PickerEmpty,
} from "../issues/components/pickers/property-picker";
import { useAuthStore } from "@multica/core/auth";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  ContentEditor,
  type ContentEditorRef,
  useFileDropZone,
  FileDropOverlay,
  useUploadGate,
  useComposerSubmit,
} from "../editor";
import { useIssueCreateUploads } from "./use-issue-create-uploads";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { useT } from "../i18n";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { SourceContextPreviewCard, useSourceContextFailureMessage } from "./source-context-preview";

// ActorSelection —— 本次创建的「执行者」：要么是某个 agent，要么是某个小队。
type ActorSelection =
  | { type: "agent"; id: string }
  | { type: "squad"; id: string };

/**
 * 创建 issue 弹窗的 Agent 模式面板主体（Quick Create）。
 *
 * 只渲染内部内容；外围的 `<Dialog>` 和 `<DialogContent>`（Portal + Overlay + Popup）
 * 由 CreateIssueDialog 持有，因此切换模式只替换本面板。把 Portal 上提到外壳，正是
 * 消除「关闭→打开」动画闪烁的原因——即使 Dialog 根节点还开着，一旦 DialogContent
 * 被重新挂载，Base UI 就会重放 Popup 的进出场动画。
 *
 * `onSwitchMode` 由外壳接线——面板调用它时可携带一份数据（目前是 `project_id`）。
 * 共享草稿 store 携带描述与 agent 一起跨过 agent→manual 的翻转；project_id 则走
 * manual→agent 同一条携带通道，所以手动面板能从 `data?.project_id` 读到它，
 * 无需另开一个 store。
 */
export function AgentCreatePanel({
  onClose,
  onSwitchMode,
  data,
  isExpanded,
  setIsExpanded,
}: {
  onClose: () => void;
  onSwitchMode?: (carry?: Record<string, unknown> | null) => void;
  data?: Record<string, unknown> | null;
  /** 提升到外壳，让 DialogContent 的「模式感知」className 能响应它——与
   *  ManualCreatePanel 同一模式。跨模式共享，用户切 agent/manual 时展开偏好不丢。 */
  isExpanded: boolean;
  setIsExpanded: (v: boolean) => void;
}) {
  const { t } = useT("modals");
  const { t: tIssues } = useT("issues");
  const { t: tProjects } = useT("projects");
  const sendShortcut = useShortcut("send");
  const workspaceName = useCurrentWorkspace()?.name;
  const workspacePaths = useWorkspacePaths();
  const wsId = useWorkspaceId();
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
  const userId = useAuthStore((s) => s.user?.id);
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));
  // 读 `isSuccess` 而不是数组长度：下面「清除失效 projectId」的清扫逻辑需要区分
  // 「还在加载」和「已加载但为空」。只读 length 会把两者都当成空数组，从而在每次
  // 打开弹窗时把合法的持久化偏好误清掉。
  const { data: projects = [], isSuccess: projectsLoaded } = useQuery(
    projectListOptions(wsId),
  );

  const memberRole = useMemo(
    () => members.find((m) => m.user_id === userId)?.role,
    [members, userId],
  );

  // 「可见」= 未归档 且 当前用户可指派。小队继承其队长 agent 的可达性：后端总是把
  // 小队的选择路由给队长执行，所以隐藏「队长不可见」的小队，能让选择器展示的结果
  // 与服务器真正会接受的范围保持一致。
  const visibleAgents = useMemo(
    () =>
      agents.filter(
        (a) =>
          !a.archived_at &&
          isAgentRuntimeBound(a) &&
          canAssignAgent(a, userId, memberRole),
      ),
    [agents, userId, memberRole],
  );
  const visibleAgentIds = useMemo(
    () => new Set(visibleAgents.map((a) => a.id)),
    [visibleAgents],
  );
  const visibleSquads = useMemo(
    () =>
      squads.filter(
        (s) => !s.archived_at && visibleAgentIds.has(s.leader_id),
      ),
    [squads, visibleAgentIds],
  );

  const lastActorType = useQuickCreateStore((s) => s.lastActorType);
  const lastActorId = useQuickCreateStore((s) => s.lastActorId);
  const setLastActor = useQuickCreateStore((s) => s.setLastActor);
  const visibleFields = useIssueCreateSettingsStore((s) => s.quickCreateFields);
  const keepOpen = useQuickCreateStore((s) => s.keepOpen);
  const setKeepOpen = useQuickCreateStore((s) => s.setKeepOpen);
  const setLastMode = useCreateModeStore((s) => s.setLastMode);
  // agent 草稿（prompt + actor）与共享字段（项目/优先级/截止日期/附件）都存放在
  // 统一的 issue 创建草稿里，因此在手动表单间来回切换也能完整保留。
  const draft = useIssueDraftStore((s) => s.draft);
  const setShared = useIssueDraftStore((s) => s.setShared);
  const setManual = useIssueDraftStore((s) => s.setManual);
  const setAgent = useIssueDraftStore((s) => s.setAgent);
  const setActiveMode = useIssueDraftStore((s) => s.setActiveMode);
  const clearDraft = useIssueDraftStore((s) => s.clearDraft);

  // 把候选执行者与「当前可见的 agent/小队」比对一遍。当候选在当前工作区里已不存在时
  // （被删除、归档、权限被收回等）返回 null，让调用方沿着种子链继续找下一个候选。
  const resolveActor = useCallback(
    (
      type: QuickCreateActorType | "agent" | "squad" | null | undefined,
      id: string | null | undefined,
    ): ActorSelection | null => {
      if (!type || !id) return null;
      if (type === "squad" && visibleSquads.some((s) => s.id === id)) {
        return { type: "squad", id };
      }
      if (type === "agent" && visibleAgentIds.has(id)) {
        return { type: "agent", id };
      }
      return null;
    },
    [visibleSquads, visibleAgentIds],
  );

  const seedActor = useCallback((): ActorSelection | null => {
    // 种子优先级：调用方带进来的优先（例如外壳用 `agent_id` / `squad_id` 预置），
    // 其次是持久化的 agent 草稿、上一次成功选择的执行者，最后回退到第一个可见 agent。
    const dataAgent = data?.agent_id as string | undefined;
    const dataSquad = data?.squad_id as string | undefined;
    return (
      resolveActor("agent", dataAgent) ||
      resolveActor("squad", dataSquad) ||
      resolveActor(draft.agent.actorType, draft.agent.actorId) ||
      resolveActor(lastActorType, lastActorId) ||
      (visibleAgents[0]
        ? ({ type: "agent", id: visibleAgents[0].id } as const)
        : null)
    );
  }, [
    resolveActor,
    data?.agent_id,
    data?.squad_id,
    draft.agent.actorType,
    draft.agent.actorId,
    lastActorType,
    lastActorId,
    visibleAgents,
  ]);

  const [actor, setActor] = useState<ActorSelection | null>(() => seedActor());

  // 可见列表解析完成后再补一次种子（首次渲染时查询可能还是空的）。
  useEffect(() => {
    if (actor && resolveActor(actor.type, actor.id)) return;
    setActor(seedActor());
  }, [actor, resolveActor, seedActor]);

  const selectedAgent = useMemo<Agent | undefined>(() => {
    if (!actor) return undefined;
    if (actor.type === "agent") return visibleAgents.find((a) => a.id === actor.id);
    const squad = visibleSquads.find((s) => s.id === actor.id);
    if (!squad) return undefined;
    return visibleAgents.find((a) => a.id === squad.leader_id);
  }, [actor, visibleAgents, visibleSquads]);

  const selectedSquad = useMemo<Squad | undefined>(() => {
    if (actor?.type !== "squad") return undefined;
    return visibleSquads.find((s) => s.id === actor.id);
  }, [actor, visibleSquads]);

  // 未完成的选项存放在共享的 issue 创建草稿里。「最近一次成功选择」的执行者始终是
  // 独立的兜底来源，因此清空草稿不会覆盖掉由真实创建确立的默认值。
  //
  // 项目字段恰好有两个种子，且都带着明确的用户意图：弹窗从哪个项目页（或手动面板）
  // 打开的，以及用户自己未完成的草稿。故意不从「上一次创建」取——见 quick-create-store
  // （MUL-5862）。
  const [projectId, setProjectId] = useState<string | null>(() => {
    const seed = (data?.project_id as string | undefined) ?? draft.shared.projectId;
    return seed ?? null;
  });
  const [priority, setPriority] = useState<IssuePriority>(
    (data?.priority as IssuePriority | undefined) ?? draft.shared.priority,
  );
  const [dueDate, setDueDate] = useState<string | null>(
    (data?.due_date as string | undefined) ?? draft.shared.dueDate,
  );
  const [fieldPickerOpen, setFieldPickerOpen] = useState<QuickCreateField | null>(null);
  // 本地 state 与共享草稿始终同步移动，所以选择器各行的改动和胶囊上的「快速清除」
  // 都统一走这里。
  const commitProject = (next: string | null) => {
    setProjectId(next);
    setShared({ projectId: next ?? undefined });
  };

  // 父 issue 上下文——当弹窗是从某 issue 上的「添加子 issue」入口打开时，由
  // openCreateSubIssue 种下。我们只透传它（不当作可编辑的表单字段），以便 manual→agent
  // 的翻转保留「创建子 issue」的意图；agent 面板从不把它暴露成选择器。标识（identifier）
  // 只是尽力而为的展示信息——真正权威的是 UUID，后端/agent 用它执行 `--parent <uuid>`。
  const parentIssueId = (data?.parent_issue_id as string | undefined) ?? undefined;
  const parentIssueIdentifier =
    (data?.parent_issue_identifier as string | undefined) ?? undefined;

  // 失效 id 清扫。一旦项目列表查询真正解析完成（`isSuccess`——区别于「加载中数据为
  // 空默认值」），一个不在列表里的 `projectId` 就意味着项目在别的会话中被删了。
  // 同时清掉本地 state 与未完成的草稿——草稿是剩下的唯一持久副本，不清的话下次打开
  // 会再次以同样的死值播种并提交。
  useEffect(() => {
    if (!projectsLoaded || projectId === null) return;
    if (projects.some((p) => p.id === projectId)) return;
    setProjectId(null);
    if (draft.shared.projectId === projectId) {
      setShared({ projectId: undefined });
    }
  }, [projectsLoaded, projects, projectId, draft.shared.projectId, setShared]);

  // 把持久化草稿的 activeMode 标成 agent，让之后重开弹窗、以及任何读取统一草稿
  // 的地方都知道用户正在编辑哪个表单。
  useEffect(() => {
    setActiveMode("agent");
  }, [setActiveMode]);

  // Daemon CLI 版本闸门。Agent 创建流程要求 runtime 自带的 multica CLI 不低于
  // MIN_QUICK_CREATE_CLI_VERSION；更老的 daemon 处理附件和「部分失败重试」有误
  // （见 PR #1851 / MUL-1496）。在选择器阶段就预检，让用户立刻看到提示，而不是等
  // Inbox 里的失败通知；服务端作为信任边界会再次校验。本地开发构建的 daemon
  // （git-describe 形态）在 checkQuickCreateCliVersion 内部被豁免——前后端在那里共用
  // 同一信号，因此跨 web/desktop/staging 天然一致，无需比较环境开关。
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const selectedRuntime = useMemo(
    () =>
      selectedAgent?.runtime_id
        ? runtimes.find((r) => r.id === selectedAgent.runtime_id)
        : undefined,
    [runtimes, selectedAgent?.runtime_id],
  );
  const runtimeCliVersion = readRuntimeCliVersion(selectedRuntime?.metadata);
  const baseVersionCheck = useMemo(
    () => checkQuickCreateCliVersion(runtimeCliVersion),
    [runtimeCliVersion],
  );
  const fieldVersionCheck = useMemo(
    () => checkQuickCreateFieldsCliVersion(runtimeCliVersion),
    [runtimeCliVersion],
  );
  const usesExplicitFields = priority !== "not_important_not_urgent" || dueDate !== null;
  const versionCheck = usesExplicitFields ? fieldVersionCheck : baseVersionCheck;
  const versionBlocked =
    baseVersionCheck.state !== "ok" ||
    (usesExplicitFields && fieldVersionCheck.state !== "ok");

  const initialPrompt = draft.agent.prompt || (data?.prompt as string) || "";
  // 编辑器是不受控的——提交/切换时通过 ref 读最新 markdown。`hasContent` 镜像「是否
  // 为空」，让 Create 按钮能正确禁用，而不必每个键都触发一次受控输入的重渲染。
  const editorRef = useRef<ContentEditorRef>(null);
  const [hasContent, setHasContent] = useState(initialPrompt.trim().length > 0);
  const [justSent, setJustSent] = useState(false);
  const [sentCount, setSentCount] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const uploadGate = useUploadGate(editorRef);
  // 协调器持有的共享草稿池上传（MUL-5181, L2）：粘贴进 prompt 的文件能跨弹窗关闭、
  // 跨模式切换存活，退出登录时中止，刷新后被丢弃。`gate` 用池里的占位项扩宽编辑器闸门。
  const {
    attachments: pendingAttachments,
    handleUpload: handleUploadFile,
    gate,
  } = useIssueCreateUploads("agent", uploadGate, editorRef);
  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => editorRef.current?.uploadFile(f)),
  });

  useEffect(() => {
    // 延迟聚焦，让它落在弹窗的焦点陷阱稳定之后——否则下一拍焦点可能被陷阱弹回
    // 头部第一个可聚焦按钮。
    const id = requestAnimationFrame(() => editorRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, []);

  // Agent 创建走共享的「先 await 后渲染」composer 契约（单飞 ref、提交时重查上传、
  // 加锁转圈、await→boolean、只在接受时清空）。prompt 就是编辑器内容，所以直接套用。
  // 过期提交守卫（MUL-5181 P0）：issue 草稿是单例 store。用户中途关掉弹窗后，一次
  // 迟到的成功绝不能清掉重开后新打的草稿——规则见 ManualCreatePanel。
  const mountedRef = useRef(true);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const submittedDraftRef = useRef<IssueCreateDraft | null>(null);
  // 由 `onAccepted` 在「保持打开并清空编辑器」的那个分支里置位，`afterAccepted` 读回。
  // 走关闭分支时不能去聚焦一个即将随弹窗卸载的编辑器。
  const refocusAfterAcceptRef = useRef(false);

  const composer = useComposerSubmit({
    editorRef,
    uploadGate: gate,
    onSubmit: async (md): Promise<boolean> => {
      // 按钮已用 !actor / versionBlocked 禁用，但 ⌘+Enter 会绕过按钮——这里再次
      // 把关，失败时草稿原样保留。
      if (!actor || versionBlocked || (anchorCommentId && !sourcePreview)) return false;
      // 快照前先 flush prompt 编辑器挂起的防抖——见 ManualCreatePanel。
      const pendingPrompt = editorRef.current?.flushPendingUpdate?.();
      if (pendingPrompt != null) setAgent({ prompt: pendingPrompt });
      submittedDraftRef.current = useIssueDraftStore.getState().draft;
      const activeAttachmentIds = pendingAttachments
        .filter((a) => contentReferencesAttachment(md, a))
        .map((a) => a.id);
      setError(null);
      try {
        if (anchorCommentId && sourcePreview) {
          await api.createCommentSubIssue(anchorCommentId, {
            mode: "agent",
            capture_token: sourcePreview.capture_token,
            quick_create: {
              ...(actor.type === "agent"
                ? { agent_id: actor.id }
                : { squad_id: actor.id }),
              prompt: md,
              project_id: projectId ?? undefined,
              ...(priority !== "not_important_not_urgent" ? { priority } : {}),
              ...(dueDate ? { due_date: dueDate } : {}),
              ...(activeAttachmentIds.length > 0 ? { attachment_ids: activeAttachmentIds } : {}),
            },
          });
        } else {
          await api.quickCreateIssue({
            ...(actor.type === "agent"
              ? { agent_id: actor.id }
              : { squad_id: actor.id }),
            prompt: md,
            project_id: projectId ?? undefined,
            ...(priority !== "not_important_not_urgent" ? { priority } : {}),
            ...(dueDate ? { due_date: dueDate } : {}),
            parent_issue_id: parentIssueId,
            ...(activeAttachmentIds.length > 0 ? { attachment_ids: activeAttachmentIds } : {}),
          });
        }
        setLastActor(actor.type, actor.id);
        setLastMode("agent");
        toast.success(t(($) => $.create_issue.agent.toast_sent), {
          duration: 4000,
        });
        return true;
      } catch (e) {
        // 服务端对弹窗关心的「结构化拒绝」路径返回 422 + { code, ... }。把原因就地
        // 展示在弹窗内，用户不必离开当前流程就能去切换可用 agent 或升级 daemon。
        if (e instanceof ApiError && e.body && typeof e.body === "object") {
          const body = e.body as {
            code?: string;
            reason?: string;
            current_version?: string;
            min_version?: string;
          };
          if (body.code === "agent_unavailable") {
            setError(body.reason || t(($) => $.create_issue.agent.error_agent_unavailable_fallback));
            return false;
          }
          if (body.code === "daemon_version_unsupported") {
            // 竞态兜底：选择器的预检通常能拦住，但 runtime 可能在「预检」与「提交」之间
            // 用一个更旧的 CLI 静默重新注册。文案与内联提示保持一致。
            const cur = body.current_version || "unknown";
            setError(
              t(($) => $.create_issue.agent.error_daemon_version, {
                current: cur,
                min: body.min_version || versionCheck.min,
              }),
            );
            return false;
          }
          if (anchorCommentId && (
            body.code === "source_context_changed"
            || body.code === "anchor_comment_deleted"
            || body.code === "source_issue_deleted"
          )) {
            await refetchSourceContext?.();
            setError(sourceContextFailureMessage(e) ?? tIssues(($) => $.source_context.error_source_changed));
            return false;
          }
          if (anchorCommentId && body.code === "source_context_quick_create_unsupported") {
            setError(tIssues(($) => $.source_context.error_agent_unsupported));
            return false;
          }
          if (anchorCommentId && body.code === "source_context_server_unsupported") {
            setError(tIssues(($) => $.source_context.error_server_unsupported));
            return false;
          }
          if (anchorCommentId && body.code === "source_context_too_large") {
            setError(sourceContextFailureMessage(e) ?? tIssues(($) => $.source_context.error_too_large));
            return false;
          }
        }
        setError(
          e instanceof Error && e.message
            ? e.message
            : t(($) => $.create_issue.agent.error_unknown),
        );
        return false;
      }
    },
    // 连续创建模式会把光标放回编辑器，让下一条 prompt 能立刻输入。故意不传
    // `containerRef`：这是弹窗，自身的焦点陷阱已经圈定了焦点能去的地方，而「夺回焦点」
    // 正是保持打开模式的意义。
    afterAccepted: () => (refocusAfterAcceptRef.current ? "refocus" : "none"),
    onAccepted: () => {
      refocusAfterAcceptRef.current = false;
      // 一次成功创建会终结整份草稿（共享 + manual + agent）；「最近成功」的 actor/
      // project 偏好在 onSubmit 里已经存好了。成功只能消费它提交的那份草稿：先 flush
      // 编辑器挂起的防抖，再只清「未被改动」的草稿——请求途中或重开弹窗产生的编辑保留。
      const latePrompt = editorRef.current?.flushPendingUpdate?.();
      if (latePrompt != null) setAgent({ prompt: latePrompt });
      const untouched =
        useIssueDraftStore.getState().draft === submittedDraftRef.current;
      if (untouched) clearDraft();
      // 请求期间的编辑会保留下来，面板停在那份更新的草稿上。
      if (!mountedRef.current || !untouched) return;
      if (keepOpen) {
        // 保持打开以连续创建——清空编辑器，用户能立刻键入下一条 prompt。
        editorRef.current?.clearContent();
        setHasContent(false);
        setSentCount((c) => c + 1);
        setJustSent(true);
        setTimeout(() => setJustSent(false), 1500);
        refocusAfterAcceptRef.current = true;
      } else {
        onClose();
      }
    },
  });
  const submit = () => {
    void composer.submit();
  };
  const submitting = composer.submitting;

  // 切到手动表单但「不销毁」agent 草稿。agent 槽位（prompt + actor）原样保留，
  // 之后 manual→agent 翻回来能逐字恢复。项目/优先级/截止日期本来就在共享槽位里，
  // 免费跟着过去。只有两项一次性「辅助初始化」在手动槽位仍为空时执行：用 prompt 种子
  // 描述、用所选 actor 种子负责人。父 issue 上下文不持久化（属于单次调用意图），
  // 因此走携带通道。
  const switchToManual = () => {
    // prompt 会在辅助初始化时拷进手动描述；上传中途这段正文已经丢了待传的图片
    // （见 switchToAgent），所以同样要上闸。
    if (gate.isBlocked()) return;
    // 把共享字段写进草稿，让手动面板从那里读——本地 state 可能持有从 `data` 做种子
    // 的值，却从未经过选择器写出。
    setShared({ projectId: projectId ?? undefined, priority, dueDate });
    if (!draft.manual.description.trim()) {
      const md = editorRef.current?.getMarkdown() ?? "";
      if (md) setManual({ description: md });
    }
    if (!draft.manual.assigneeId && actor) {
      setManual({ assigneeType: actor.type, assigneeId: actor.id });
    }
    setLastMode("manual");
    setActiveMode("manual");
    const carry: Record<string, unknown> = {};
    if (parentIssueId) carry.parent_issue_id = parentIssueId;
    if (parentIssueIdentifier) carry.parent_issue_identifier = parentIssueIdentifier;
    onSwitchMode?.(Object.keys(carry).length > 0 ? carry : null);
  };

  // 字段可见性存放在「设置 → Issue」。离开前先把 prompt 草稿落库，让用户已输入的内容
  // 在往返后不丢，然后关闭——否则弹窗会一直叠在设置页上方。
  const openFieldSettings = (e: React.MouseEvent) => {
    // 无论哪种情况都先持久化草稿；但只有「当前页内跳转」才关弹窗——带修饰键的点击
    // 会在新标签页打开设置，弹窗保持原位。
    setAgent({ prompt: editorRef.current?.getMarkdown() ?? "" });
    if (resolveClickIntent(e) !== "push") return;
    onClose();
  };

  return (
    <>
        <DialogTitle className="sr-only">{t(($) => $.create_issue.sr_agent)}</DialogTitle>

        {/* 弹窗头部——工作区面包屑 + 展开/收起与关闭按钮 */}
        <div className="flex items-center justify-between px-5 pt-3 pb-2 shrink-0">
          <div className="flex items-center gap-1.5 text-caption">
            <span className="text-muted-foreground">{workspaceName}</span>
            <ChevronRight className="size-3 text-faint-foreground" />
            <span className="font-medium">{t(($) => $.create_issue.agent_breadcrumb)}</span>
          </div>
          {/* 用原生 `title` 而不是 Base UI Tooltip——Tooltip 在键盘聚焦时弹出，而弹窗的
              焦点陷阱在挂载瞬间会把焦点落到第一个可聚焦元素上，导致每次打开 tooltip
              都会自动弹一次。展开按钮同样用这个变通方案。 */}
          <div className="flex items-center gap-1">
            <button
              type="button"
              onClick={() => setIsExpanded(!isExpanded)}
              title={isExpanded ? t(($) => $.common.collapse_tooltip) : t(($) => $.common.expand_tooltip)}
              aria-label={isExpanded ? t(($) => $.common.collapse_tooltip) : t(($) => $.common.expand_tooltip)}
              className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
            >
              {isExpanded ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
            </button>
            <button
              type="button"
              onClick={onClose}
              title={t(($) => $.common.close)}
              aria-label={t(($) => $.common.close)}
              className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
            >
              <XIcon className="size-4" />
            </button>
          </div>
        </div>

        {/* Actor 选择器——agent 与小队在同一份可搜索列表里。小队在后端路由到其队长
            agent：队长以小队叠加的 Operating Protocol 执行 quick-create 流程，所以
            选小队等于「让这个小队来提单」。 */}
        <div className="px-5 pt-1 pb-2 shrink-0">
          <ActorPicker
            actor={actor}
            visibleAgents={visibleAgents}
            visibleSquads={visibleSquads}
            selectedAgent={selectedAgent}
            selectedSquad={selectedSquad}
            onPick={(next) => {
              setActor(next);
              setAgent({ actorType: next.type, actorId: next.id });
              setError(null);
            }}
            t={t}
          />
        </div>

        {selectedAgent && versionBlocked && (
          <div className="mx-5 mb-2 shrink-0 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-caption text-amber-700 dark:text-amber-300">
            {versionCheck.state === "missing"
              ? versionCheck.current
                ? t(($) => $.create_issue.agent.version_unrecognized, {
                    current: versionCheck.current,
                    min: versionCheck.min,
                  })
                : t(($) => $.create_issue.agent.version_missing, { min: versionCheck.min })
              : t(($) => $.create_issue.agent.version_below, {
                  current: versionCheck.current,
                  min: versionCheck.min,
                })}
          </div>
        )}

        {/* Prompt——与 Advanced 用法同一套富文本编辑器，粘贴/拖拽图片、@提及、排版
            全部可用。dropZone 外壳让拖拽上传与粘贴并存。 */}
        {/* `flex-1 min-h-0 overflow-y-auto`：让编辑器区吸收（现在有最大高度约束的）
            DialogContent 里的剩余纵向空间并在内部滚动。没有它，粘贴一张图片会把编辑器
            撑到没有上限，把弹窗推出视口。 */}
        <div
          {...dropZoneProps}
          className="relative px-5 pb-3 flex flex-1 min-h-[140px] overflow-y-auto"
        >
          <ContentEditor
            ref={editorRef}
            defaultValue={initialPrompt}
            placeholder={anchorCommentId
              ? t(($) => $.create_issue.agent.source_context_prompt_placeholder)
              : t(($) => $.create_issue.agent.prompt_placeholder)}
            onUpdate={(md) => {
              setHasContent(md.trim().length > 0);
              setAgent({ prompt: md });
            }}
            onUploadFile={handleUploadFile}
            onUploadingChange={uploadGate.onUploadingChange}
            attachments={pendingAttachments}
            onSubmit={submit}
            debounceMs={150}
          />
          {isDragOver && <FileDropOverlay />}
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

        {error && (
          <div className="px-5 pb-2 text-caption text-destructive">{error}</div>
        )}

        {/* 属性工具栏——项目默认可见；优先级与截止日期在设置里打开或填了值之前，收在
            溢出菜单后面。未完成的选项保持在「工作区级」持久化。
            当弹窗是从某 issue 上的「添加子 issue」打开时，同一行会出现一枚只读胶囊，
            告诉用户新 issue 将作为那个父 issue 的子 issue 提交——agent 会静默处理
            挂接，但把关系亮出来可以避免「它到底被建到哪去了」的困惑。故意做成不可编辑：
            改父级是父 issue 自己的「设置父级」操作，不该成为 quick-create 流程里的一个
            旋钮。 */}
        <div className="flex items-center gap-1.5 px-4 pb-2 shrink-0 flex-wrap">
          {(visibleFields.includes("project") ||
            projectId !== null ||
            fieldPickerOpen === "project") && (
            <ProjectPicker
              projectId={projectId}
              onUpdate={(u) => commitProject(u.project_id ?? null)}
              triggerRender={
                <ClearablePillButton
                  onClear={projectId !== null ? () => commitProject(null) : undefined}
                  clearLabel={tProjects(($) => $.picker.clear_aria)}
                />
              }
              align="start"
              open={fieldPickerOpen === "project" ? true : undefined}
              onOpenChange={(open) => setFieldPickerOpen(open ? "project" : null)}
            />
          )}
          {(visibleFields.includes("priority") ||
            priority !== "not_important_not_urgent" ||
            fieldPickerOpen === "priority") && (
            <PriorityPicker
              priority={priority}
              onUpdate={(updates) => {
                if (updates.priority) {
                  setPriority(updates.priority);
                  setShared({ priority: updates.priority });
                }
              }}
              triggerRender={<PillButton />}
              align="start"
              open={fieldPickerOpen === "priority" ? true : undefined}
              onOpenChange={(open) => setFieldPickerOpen(open ? "priority" : null)}
            />
          )}
          {(visibleFields.includes("due_date") ||
            dueDate !== null ||
            fieldPickerOpen === "due_date") && (
            <DueDatePicker
              dueDate={dueDate}
              onUpdate={(updates) => {
                const next = updates.due_date ?? null;
                setDueDate(next);
                setShared({ dueDate: next });
              }}
              triggerRender={<PillButton />}
              align="start"
              open={fieldPickerOpen === "due_date" ? true : undefined}
              onOpenChange={(open) => setFieldPickerOpen(open ? "due_date" : null)}
            />
          )}
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <PillButton
                  aria-label={t(($) => $.create_issue.agent.more_fields_aria)}
                  title={t(($) => $.create_issue.agent.more_fields_aria)}
                >
                  <MoreHorizontal className="size-3.5" />
                </PillButton>
              }
            />
            <DropdownMenuContent align="start" className="w-52">
              {!visibleFields.includes("project") && projectId === null && (
                <DropdownMenuItem onClick={() => setFieldPickerOpen("project")}>
                  <FolderKanban className="size-3.5 text-muted-foreground" />
                  {t(($) => $.create_issue.agent.set_project)}
                </DropdownMenuItem>
              )}
              {!visibleFields.includes("priority") && priority === "not_important_not_urgent" && (
                <DropdownMenuItem onClick={() => setFieldPickerOpen("priority")}>
                  <PriorityIcon priority="not_important_not_urgent" className="size-3.5" />
                  {t(($) => $.create_issue.agent.set_priority)}
                </DropdownMenuItem>
              )}
              {!visibleFields.includes("due_date") && dueDate === null && (
                <DropdownMenuItem onClick={() => setFieldPickerOpen("due_date")}>
                  <CalendarDays className="size-3.5 text-muted-foreground" />
                  {t(($) => $.create_issue.agent.set_due_date)}
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem
                render={
                  <AppLink
                    href={`${workspacePaths.settings()}?tab=issue`}
                    onClick={openFieldSettings}
                  />
                }
              >
                <Settings2 className="size-3.5 text-muted-foreground" />
                {t(($) => $.create_issue.agent.customize_fields)}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          {parentIssueId && (
            <span
              data-testid="agent-sub-issue-chip"
              className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-caption text-muted-foreground"
              title={t(($) => $.create_issue.agent.sub_issue_of, {
                identifier: parentIssueIdentifier ?? "",
              })}
            >
              {t(($) => $.create_issue.agent.sub_issue_of, {
                identifier: parentIssueIdentifier ?? "",
              })}
            </span>
          )}
        </div>

        {/* Footer。两种布局，共享同一份平铺的子元素列表：
            - 手机端是 2×2 网格——上行「附件 / 切手动」，下行「连续创建开关 / 创建」。
              排成单行的话，四个控件需要约 383px，而 430px 手机扣掉内边距只剩 398px，
              显得拥挤且在 ~410px 以下直接换行（MUL-6236）。
            - 从 `sm` 起恢复原本的单行 flex：附件组上的 `mr-auto` 还原了当初子元素是
              两个包装 div 时 `justify-between` 的效果，`justify-self-end` 在 flex 项上
              会失效。 */}
        <div className="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-2.5 border-t px-4 py-3 shrink-0 sm:flex sm:flex-wrap">
          <div className="flex min-h-7 items-center gap-2 sm:mr-auto">
            {/* 上传中故意不禁用它：每个文件是独立队列项，排队第二个是安全的——非要等
                第一个落地才能传下一个纯属白费功夫。 */}
            <FileUploadButton
              size="sm"
              multiple
              onSelect={(file) => editorRef.current?.uploadFile(file)}
            />
            {keepOpen && sentCount > 0 && (
              <span className="text-caption text-emerald-600 dark:text-emerald-400">
                {t(($) => $.create_issue.agent.sent_count, { count: sentCount })}
              </span>
            )}
          </div>
          <button
            type="button"
            onClick={switchToManual}
            disabled={gate.uploading}
            aria-disabled={gate.uploading || undefined}
            aria-busy={gate.uploading || undefined}
            title={t(($) => $.create_issue.switch_to_manual_tooltip)}
            className="flex shrink-0 items-center gap-1.5 justify-self-end text-caption px-2 py-1 rounded-sm text-muted-foreground hover:text-foreground hover:bg-accent/60 transition-colors cursor-pointer disabled:cursor-not-allowed disabled:opacity-50"
          >
            <ArrowLeftRight className="size-3.5" />
            {t(($) => $.create_issue.switch_to_manual)}
          </button>
          <label className="flex shrink-0 items-center gap-1.5 text-caption text-muted-foreground cursor-pointer select-none">
            <Switch
              size="sm"
              checked={keepOpen}
              onCheckedChange={setKeepOpen}
            />
            {t(($) => $.create_issue.create_another)}
          </label>
          <Button
            size="sm"
            onClick={submit}
            disabled={!hasContent || !actor || submitting || versionBlocked || gate.uploading || (!!anchorCommentId && !sourcePreview)}
            aria-disabled={gate.uploading || undefined}
            // 发送同样是忙碌态，不只是上传中。
            aria-busy={gate.uploading || submitting || undefined}
            title={
              versionBlocked
                ? t(($) => $.create_issue.agent.version_blocked_tooltip, { min: versionCheck.min })
                : undefined
            }
            className={cn(
              "justify-self-end min-w-28",
              justSent && "!bg-emerald-600 !text-white",
            )}
          >
            {submitting ? t(($) => $.create_issue.agent.sending) : gate.uploading ? t(($) => $.create_issue.agent.uploading) : justSent ? (
              <span className="flex items-center gap-1"><Check className="size-3.5" />{t(($) => $.create_issue.agent.sent_label)}</span>
            ) : (
              <>
                {t(($) => $.create_issue.agent.submit)}
                {sendShortcut ? (
                  // 触摸手机没有 ⌘ 键、footer 行又最紧凑——在 footer 换行的同一个断点
                  // 下隐藏这个快捷键提示。
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
        </div>
    </>
  );
}

/**
 * ActorPicker ——「Created by」触发器 + 可搜索的 agent/小队列表弹层。
 *
 * 放在本文件（而不是 issues/components/pickers 下），因为它用泛型 PropertyPicker
 * 组装了一个「贴合 quick-create」的触发器，样式对齐弹窗头部行——提升到共享目录会
 * 招来「复用它」的压力，而这套 UI 是特意只为此处表面调出来的。
 */
function ActorPicker({
  actor,
  visibleAgents,
  visibleSquads,
  selectedAgent,
  selectedSquad,
  onPick,
  t,
}: {
  actor: ActorSelection | null;
  visibleAgents: Agent[];
  visibleSquads: Squad[];
  selectedAgent: Agent | undefined;
  selectedSquad: Squad | undefined;
  onPick: (next: ActorSelection) => void;
  t: ReturnType<typeof useT<"modals">>["t"];
}) {
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLowerCase();

  const filteredAgents = useMemo(
    () => visibleAgents.filter((a) => a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query)),
    [visibleAgents, query],
  );
  const filteredSquads = useMemo(
    () => visibleSquads.filter((s) => s.name.toLowerCase().includes(query) || matchesPinyin(s.name, query)),
    [visibleSquads, query],
  );

  const displayLabel = selectedSquad?.name ?? selectedAgent?.name;
  const displayActor: ActorSelection | null = selectedSquad
    ? { type: "squad", id: selectedSquad.id }
    : selectedAgent
      ? { type: "agent", id: selectedAgent.id }
      : null;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(v: boolean) => {
        setOpen(v);
        if (!v) setFilter("");
      }}
      width="w-64"
      align="start"
      searchable
      searchPlaceholder={t(($) => $.create_issue.agent.search_placeholder)}
      onSearchChange={setFilter}
      trigger={
        <span className="flex items-center gap-2 text-caption text-muted-foreground hover:text-foreground transition-colors">
          <span>{t(($) => $.create_issue.agent.created_by)}</span>
          {displayActor && displayLabel ? (
            <span className="flex items-center gap-1.5 text-foreground">
              <ActorAvatar
                actorType={displayActor.type}
                actorId={displayActor.id}
                size="sm"
              />
              {displayLabel}
            </span>
          ) : (
            <span>{t(($) => $.create_issue.agent.pick_an_agent)}</span>
          )}
        </span>
      }
    >
      {filteredAgents.length === 0 && filteredSquads.length === 0 ? (
        query ? (
          <PickerEmpty />
        ) : (
          <div className="px-2 py-1.5 text-caption text-muted-foreground">
            {t(($) => $.create_issue.agent.no_agents)}
          </div>
        )
      ) : (
        <>
          {filteredAgents.length > 0 && (
            <PickerSection label={t(($) => $.create_issue.agent.agents_group)}>
              {filteredAgents.map((a) => (
                <PickerItem
                  key={a.id}
                  selected={actor?.type === "agent" && actor.id === a.id}
                  onClick={() => {
                    onPick({ type: "agent", id: a.id });
                    setOpen(false);
                  }}
                >
                  <ActorAvatar actorType="agent" actorId={a.id} size="sm" />
                  <span className="truncate">{a.name}</span>
                </PickerItem>
              ))}
            </PickerSection>
          )}
          {filteredSquads.length > 0 && (
            <PickerSection label={t(($) => $.create_issue.agent.squads_group)}>
              {filteredSquads.map((s) => (
                <PickerItem
                  key={s.id}
                  selected={actor?.type === "squad" && actor.id === s.id}
                  onClick={() => {
                    onPick({ type: "squad", id: s.id });
                    setOpen(false);
                  }}
                >
                  <ActorAvatar actorType="squad" actorId={s.id} size="sm" />
                  <span className="truncate">{s.name}</span>
                </PickerItem>
              ))}
            </PickerSection>
          )}
        </>
      )}
    </PropertyPicker>
  );
}
