// autopilot 创建/编辑弹窗：左栏标题与提示词正文（runbook），右栏配置（执行者 /
// 输出模式 / 项目 / 订阅者 / 触发类型与计划或 webhook 过滤）。提交时自动先存
// autopilot 主体、再同步其调度触发；webhook 创建成功后弹窗不关闭，而是切换到展示
// 新 URL 的确认态。编辑模式可展开为更大尺寸、可管理协作者访问。触发类型只在创建
// 时可切换（编辑不支持就地互转，按 PLAN.md 是"删旧建新"）。

"use client";

import { useId, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  FilePlus2,
  FolderKanban,
  Maximize2,
  Minimize2,
  Play,
  Plus,
  Rocket,
  Users,
  Webhook,
  X as XIcon,
  Zap,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
  PopoverHeader,
  PopoverTitle,
  PopoverDescription,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions, squadListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import {
  useCreateAutopilot,
  useCreateAutopilotTrigger,
  useUpdateAutopilot,
  useUpdateAutopilotTrigger,
} from "@multica/core/autopilots/mutations";
import { buildAutopilotWebhookUrl } from "@multica/core/autopilots";
import { api } from "@multica/core/api";
import type {
  AutopilotAssigneeType,
  AutopilotCollaborator,
  AutopilotExecutionMode,
  AutopilotTrigger,
} from "@multica/core/types";
import { TitleEditor, ContentEditor, type TitleEditorRef } from "../../editor";
import { ActorAvatar } from "../../common/actor-avatar";
import { SegmentedToggle } from "../../common/segmented-toggle";
import { ProjectPicker } from "../../projects/components/project-picker";
import { ProjectIcon } from "../../projects/components/project-icon";
import { AgentPicker, type AssigneeSelection } from "./pickers/agent-picker";
import { SubscriberMultiSelect } from "./subscriber-multi-select";
import { AutopilotAccessManager } from "./autopilot-access-manager";
import { ScheduleEditor } from "./schedule-editor/schedule-editor";
import { getDefaultScheduleConfig, type ScheduleConfig } from "./schedule-editor/model";
import { browserTimezone } from "../../common/timezone-select";
import { parseCron, toCron } from "./schedule-editor/cron-mapping";
import { useScheduleSubmitGate } from "./schedule-editor/validate";
import { WebhookEventFilterSection } from "./webhook-event-filter-section";
import { WebhookUrlField } from "./webhook-url-field";
import { useT } from "../../i18n";
import { formatSchedulePartialFailureToast } from "./autopilot-dialog-toast";
import type { WebhookEventFilter } from "@multica/core/types";

// ---------------------------------------------------------------------------
// 类型
// ---------------------------------------------------------------------------

// 编辑弹窗的初始表单值（autopilot 主体字段）。
export interface AutopilotInitial {
  title: string;
  description: string;
  project_id: string | null;
  assignee_type: AutopilotAssigneeType;
  assignee_id: string;
  execution_mode: AutopilotExecutionMode;
  subscriber_user_ids?: string[];
}

// 弹窗 Props：按模式区分——create 用 `initial`/`initialSchedule` 预填；edit 额外
// 需要 autopilotId、现有 triggers/collaborators 与权限。
export type AutopilotDialogProps =
  | {
      mode: "create";
      open: boolean;
      onOpenChange: (v: boolean) => void;
      initial?: Partial<AutopilotInitial>;
      initialSchedule?: Pick<ScheduleConfig, "time" | "days">;
    }
  | {
      mode: "edit";
      open: boolean;
      onOpenChange: (v: boolean) => void;
      autopilotId: string;
      initial: AutopilotInitial;
      triggers: AutopilotTrigger[];
      collaborators: AutopilotCollaborator[];
      canManageAccess: boolean;
    };

// ---------------------------------------------------------------------------
// 静态 schema 级数据（不对用户可见）
// ---------------------------------------------------------------------------

const OUTPUT_MODE_KEYS: AutopilotExecutionMode[] = ["create_issue", "run_only"];

const OUTPUT_MODE_ICONS: Record<AutopilotExecutionMode, typeof FilePlus2> = {
  create_issue: FilePlus2,
  run_only: Play,
};

// ---------------------------------------------------------------------------
// webhook 事件过滤的脏检查
// ---------------------------------------------------------------------------

// serializeEventFilters 返回稳定 JSON 串，让编辑态的脏检查可把当前过滤器与打开时
// 的快照比较而不依赖引用相等。把空 Actions 归一化为 []，使"省略 vs 显式空"不会
// 显示成幽灵变更。
function serializeEventFilters(filters: WebhookEventFilter[]): string {
  return JSON.stringify(
    filters.map((f) => ({ event: f.event, actions: f.actions ?? [] })),
  );
}

// ---------------------------------------------------------------------------
// AutopilotDialog
// ---------------------------------------------------------------------------

/**
 * autopilot 创建/编辑弹窗。创建：标题/提示词 + 配置，提交先建 autopilot 再建
 * 触发器（webhook 成功后留在弹窗展示 URL）；编辑：预填现有值，主体与调度触发分
 * 别更新，支持事件过滤器编辑与协作者访问管理。见文件顶部导读。
 * @param props 区分 create/edit 的完整 Props（见 AutopilotDialogProps）。
 */
export function AutopilotDialog(props: AutopilotDialogProps) {
  const { t } = useT("autopilots");
  const { open, onOpenChange } = props;
  const workspaceName = useCurrentWorkspace()?.name;
  const wsId = useWorkspaceId();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const [isExpanded, setIsExpanded] = useState(false);

  const isCreate = props.mode === "create";
  const initial: Partial<AutopilotInitial> = isCreate
    ? props.initial ?? {}
    : props.initial;

  const [title, setTitle] = useState(initial.title ?? "");
  const [description, setDescription] = useState(initial.description ?? "");
  const [projectId, setProjectId] = useState<string | null>(initial.project_id ?? null);
  const [assigneeType, setAssigneeType] = useState<AutopilotAssigneeType>(
    initial.assignee_type ?? "agent",
  );
  const [assigneeId, setAssigneeId] = useState<string>(initial.assignee_id ?? "");
  const [executionMode, setExecutionMode] = useState<AutopilotExecutionMode>(
    initial.execution_mode ?? "create_issue",
  );
  const [subscriberUserIds, setSubscriberUserIds] = useState<string[]>(
    initial.subscriber_user_ids ?? [],
  );

  // 计划面板代表 autopilot 的 SCHEDULE 触发器说话，不是 `triggers[0]`——在
  // webhook/api 触发的 autopilot 上那一行不是能写 cron 的目标。
  const existingSchedule = isCreate
    ? null
    : props.triggers.find((trig) => trig.kind === "schedule") ?? null;

  const initialCfg: ScheduleConfig = (() => {
    if (isCreate) {
      const tpl = props.initialSchedule;
      const fallback = getDefaultScheduleConfig(browserTimezone());
      return tpl ? { ...fallback, ...tpl } : fallback;
    }
    if (existingSchedule?.cron_expression) {
      return parseCron(existingSchedule.cron_expression, existingSchedule.timezone ?? "UTC");
    }
    return getDefaultScheduleConfig(browserTimezone());
  })();
  const [schedule, setSchedule] = useState<ScheduleConfig>(initialCfg);

  // 编辑一个没有计划的 autopilot：面板没有可回显的东西，它显示的任何东西都会是
  // 披着"autopilot 状态"外衣的建议——而下方 `scheduleDirty` 拿建议对自身比较，
  // 于是在成功 toast 下把保存静默丢弃（MUL-5649）。计划改为被显式索取：在用户添加
  // 之前面板说 autopilot 是手动的，一旦添加，Save 就写它所显示的内容——无论用户
  // 是否动过默认值。
  const [scheduleAdded, setScheduleAdded] = useState(false);

  // 触发类型选择器。只在创建模式有意义——编辑模式不支持就地互转类型（PLAN.md
  // 把那种情况称作"删旧建新"，而非原地模糊更新），所以编辑时隐藏该切换。类型从
  // 第一条现有触发器初始化，从而渲染正确的面板而不让用户吃惊。
  const initialKind: "schedule" | "webhook" = (() => {
    if (isCreate) return "schedule";
    const first = props.triggers[0];
    if (first?.kind === "webhook") return "webhook";
    return "schedule";
  })();
  const [triggerKind, setTriggerKind] = useState<"schedule" | "webhook">(initialKind);

  // 打开时快照初始 cron/时区/事件过滤器，用于脏检查。
  const initialEventFilters: WebhookEventFilter[] =
    !isCreate && props.triggers[0]?.event_filters ? props.triggers[0].event_filters : [];
  const [eventFilters, setEventFilters] = useState<WebhookEventFilter[]>(initialEventFilters);

  const initialCronRef = useRef(toCron(initialCfg));
  const initialTimezoneRef = useRef(initialCfg.timezone);
  const initialEventFiltersRef = useRef(serializeEventFilters(initialEventFilters));
  const scheduleDirty =
    toCron(schedule) !== initialCronRef.current ||
    schedule.timezone !== initialTimezoneRef.current;
  const eventFiltersDirty =
    serializeEventFilters(eventFilters) !== initialEventFiltersRef.current;

  const firstTriggerIdRef = useRef(
    !isCreate && props.triggers[0] ? props.triggers[0].id : null,
  );
  // 计划写入所瞄准的那一行，与上面同样在挂载时快照。null 表示没有可 PATCH 的行，
  // 所以写入会新建一个。
  const scheduleTriggerIdRef = useRef(existingSchedule?.id ?? null);

  const triggerCount = isCreate ? 0 : props.triggers.length;
  const schedulePillDisabled = !isCreate && triggerCount >= 2;

  // 手动 autopilot 的空态，以及从本弹窗通往"第一个计划"的唯一入口。面板锁定时
  // （≥2 个触发器）跳过——那种情况保持渲染它一贯展示的禁用编辑器。
  const showScheduleEmptyState =
    !isCreate && existingSchedule === null && !scheduleAdded && !schedulePillDisabled;

  const selectedAssignee = useMemo(() => {
    if (!assigneeId) return null;
    if (assigneeType === "squad") {
      const squad = squads.find((s) => s.id === assigneeId);
      return squad ? { name: squad.name, description: squad.description } : null;
    }
    const agent = agents.find((a) => a.id === assigneeId);
    return agent ? { name: agent.name, description: agent.description } : null;
  }, [agents, squads, assigneeId, assigneeType]);
  const selectedProject = useMemo(
    () => projects.find((project) => project.id === projectId) ?? null,
    [projects, projectId],
  );

  const handleAssigneeChange = (next: AssigneeSelection) => {
    setAssigneeType(next.type);
    setAssigneeId(next.id);
  };

  const createAutopilot = useCreateAutopilot();
  const createTrigger = useCreateAutopilotTrigger();
  const updateAutopilot = useUpdateAutopilot();
  const updateTrigger = useUpdateAutopilotTrigger();
  const [submitting, setSubmitting] = useState(false);

  // webhook 类型创建成功后不关弹窗——切换到展示新铸造 URL（带复制/完成操作）的
  // 确认态。避免"现在去找你的 autopilot、点进去才能拿 URL"的摩擦。
  const [createdWebhookTrigger, setCreatedWebhookTrigger] = useState<AutopilotTrigger | null>(null);

  const scheduleGate = useScheduleSubmitGate(wsId);

  // 计划只在本次保存确实会写它时才门控提交。锁定的计划（≥2 触发器）或用户没动过
  // 的既有计划不会送出，因此存储表达式上的预览 400——一份服务器曾接受、现在可能
  // 拒绝的表达式（如 tzdata 丢弃的时区）——不得否决对标题/提示词/执行者的编辑。
  // 用户刚添加的计划没有可对比的已存版本：添加它本身就是这次变更。
  const scheduleWillBeWritten =
    triggerKind === "schedule" &&
    !schedulePillDisabled &&
    (isCreate || (existingSchedule !== null ? scheduleDirty : scheduleAdded));

  // 阅读顺序里第一个空的必填字段——用户填完一个，下一个浮现。这里只回应这两个：
  // 被拒的计划下面会再对服务器复检，由它 toast 真实原因。
  const missingField: "title" | "assignee" | null =
    title.trim().length === 0 ? "title" : assigneeId.length === 0 ? "assignee" : null;

  // 内联报错只在一次提交尝试后出现：打开就在用户还没走到那些字段时对其吼叫的
  // 表单，比它所取代的沉默更糟。以 `showErrors && 字段仍空` 渲染，所以填掉字段
  // 就清了它的错，无需二次提交。
  const [showErrors, setShowErrors] = useState(false);
  const titleEditorRef = useRef<TitleEditorRef>(null);
  const assigneeTriggerRef = useRef<HTMLButtonElement>(null);
  const assigneeErrorId = useId();

  const handleSubmit = async () => {
    if (submitting) return;
    if (missingField !== null) {
      // 亮出内联报错并把用户带到出错的字段；聚焦会顺带把配置列滚过去。
      setShowErrors(true);
      if (missingField === "title") titleEditorRef.current?.focus();
      else assigneeTriggerRef.current?.focus();
      return;
    }
    setSubmitting(true);
    try {
      if (scheduleWillBeWritten && !(await scheduleGate.ensureAccepted(schedule))) {
        setSubmitting(false);
        return;
      }
      if (isCreate) {
        const autopilot = await createAutopilot.mutateAsync({
          title: title.trim(),
          description: description.trim() || undefined,
          project_id: projectId,
          assignee_type: assigneeType,
          assignee_id: assigneeId,
          execution_mode: executionMode,
          subscribers: subscriberUserIds.map((user_id) => ({
            user_type: "member" as const,
            user_id,
          })),
        });
        // 触发器创建失败不推翻 autopilot 主体：部分成功也要告诉用户存了什么。
        let triggerOk = true;
        let triggerErrMessage: string | null = null;
        let webhookTrigger: AutopilotTrigger | null = null;
        try {
          if (triggerKind === "webhook") {
            webhookTrigger = await createTrigger.mutateAsync({
              autopilotId: autopilot.id,
              kind: "webhook",
              event_filters: eventFilters.length > 0 ? eventFilters : undefined,
            });
          } else {
            await createTrigger.mutateAsync({
              autopilotId: autopilot.id,
              kind: "schedule",
              cron_expression: toCron(schedule),
              timezone: schedule.timezone,
            });
          }
        } catch (err) {
          triggerOk = false;
          triggerErrMessage =
            err instanceof Error && err.message ? err.message : null;
        }
        if (triggerKind === "webhook" && webhookTrigger) {
          // 留在弹窗内就地展示 URL，用户不必先跳去详情页再复制。
          setCreatedWebhookTrigger(webhookTrigger);
          toast.success(t(($) => $.dialog.toast_created));
          return;
        }
        onOpenChange(false);
        if (triggerOk) {
          toast.success(t(($) => $.dialog.toast_created));
        } else {
          // 部分成功：autopilot 存了、计划/触发器失败。展示服务器给的原因，让用户
          // 能行动（cron 语法错、冲突等），而不是看一句通用文案。
          toast.error(formatSchedulePartialFailureToast(t, "create", triggerErrMessage));
        }
      } else {
        await updateAutopilot.mutateAsync({
          id: props.autopilotId,
          title: title.trim(),
          description: description.trim() || null,
          project_id: projectId,
          assignee_type: assigneeType,
          assignee_id: assigneeId,
          execution_mode: executionMode,
          subscribers: subscriberUserIds.map((user_id) => ({
            user_type: "member" as const,
            user_id,
          })),
        });
        let triggerOk = true;
        let triggerErrMessage: string | null = null;
        // autopilot 首触发器是 webhook 时跳过计划同步——那里没有 cron 可更新，而且
        // 计划面板对 webhook autopilot 本就不渲染。
        if (scheduleWillBeWritten) {
          const snapshottedTriggerId = scheduleTriggerIdRef.current;
          try {
            if (snapshottedTriggerId) {
              await updateTrigger.mutateAsync({
                autopilotId: props.autopilotId,
                triggerId: snapshottedTriggerId,
                cron_expression: toCron(schedule),
                timezone: schedule.timezone,
              });
            } else {
              await createTrigger.mutateAsync({
                autopilotId: props.autopilotId,
                kind: "schedule",
                cron_expression: toCron(schedule),
                timezone: schedule.timezone,
              });
            }
          } catch (err) {
            triggerOk = false;
            triggerErrMessage =
              err instanceof Error && err.message ? err.message : null;
          }
        }
        // webhook autopilot 没有计划，但用户仍可在同一弹窗编辑事件过滤器。只在打开
        // 时的快照与当前状态不同时才 PATCH。发送显式空数组会清掉服务端的过滤器
        //（三态语义——见 autopilot.go 的 UpdateAutopilotTriggerRequest）。
        if (
          triggerKind === "webhook" &&
          eventFiltersDirty &&
          firstTriggerIdRef.current
        ) {
          try {
            await updateTrigger.mutateAsync({
              autopilotId: props.autopilotId,
              triggerId: firstTriggerIdRef.current,
              event_filters: eventFilters,
            });
          } catch (err) {
            triggerOk = false;
            triggerErrMessage =
              err instanceof Error && err.message ? err.message : null;
          }
        }
        onOpenChange(false);
        if (triggerOk) {
          toast.success(t(($) => $.dialog.toast_updated));
        } else {
          toast.error(formatSchedulePartialFailureToast(t, "update", triggerErrMessage));
        }
      }
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : isCreate
            ? t(($) => $.dialog.toast_create_failed)
            : t(($) => $.dialog.toast_update_failed),
      );
    } finally {
      setSubmitting(false);
    }
  };

  const contentKey = isCreate ? "create" : props.autopilotId;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className={cn(
          "p-0 gap-0 flex flex-col overflow-hidden",
          "!transition-all !duration-300 !ease-out !-translate-y-1/2",
          "!w-[calc(100vw-2rem)]",
          isExpanded
            ? "!max-w-6xl !h-[calc(100vh-4rem)]"
            : "!max-w-5xl !h-[min(720px,calc(100vh-4rem))]",
        )}
      >
        <DialogTitle className="sr-only">
          {isCreate ? t(($) => $.dialog.sr_create) : t(($) => $.dialog.sr_edit)}
        </DialogTitle>

        {/* 头部 */}
        <div className="flex items-center justify-between px-5 pt-3 pb-2 shrink-0 border-b">
          <div className="flex items-center gap-2 text-caption">
            <div className="flex items-center gap-1.5">
              <span className="inline-flex size-5 items-center justify-center rounded-md bg-primary/15 text-primary">
                <Rocket className="size-3" />
              </span>
              <span className="font-medium text-foreground">
                {isCreate
                  ? t(($) => $.dialog.header_create)
                  : t(($) => $.dialog.header_edit)}
              </span>
            </div>
            <span className="text-faint-foreground">·</span>
            <span className="text-muted-foreground">{t(($) => $.dialog.subtitle)}</span>
            {workspaceName && (
              <>
                <ChevronRight className="size-3 text-faint-foreground" />
                <span className="text-muted-foreground">{workspaceName}</span>
              </>
            )}
          </div>
          <div className="flex items-center gap-1">
            {!isCreate && props.canManageAccess && (
              <>
                <Popover>
                  <PopoverTrigger className="flex items-center gap-1.5 rounded-sm px-2 py-1 text-caption text-muted-foreground transition-all hover:bg-accent/60 hover:text-foreground hover:opacity-100 cursor-pointer">
                    <Users className="size-3.5" />
                    <span>{t(($) => $.access.title)}</span>
                  </PopoverTrigger>
                  <PopoverContent align="end" sideOffset={6} keepMounted className="w-80">
                    <PopoverHeader>
                      <PopoverTitle>{t(($) => $.access.title)}</PopoverTitle>
                      <PopoverDescription className="text-caption">
                        {t(($) => $.access.description)}
                      </PopoverDescription>
                    </PopoverHeader>
                    <AutopilotAccessManager
                      autopilotId={props.autopilotId}
                      collaborators={props.collaborators}
                    />
                  </PopoverContent>
                </Popover>
                <span className="mx-0.5 h-4 w-px bg-border" />
              </>
            )}
            <Tooltip>
              <TooltipTrigger
                render={
                  <button
                    type="button"
                    onClick={() => setIsExpanded((v) => !v)}
                    className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
                  >
                    {isExpanded ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
                  </button>
                }
              />
              <TooltipContent side="bottom">
                {isExpanded ? t(($) => $.dialog.collapse) : t(($) => $.dialog.expand)}
              </TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger
                render={
                  <button
                    type="button"
                    onClick={() => onOpenChange(false)}
                    className="rounded-sm p-1.5 opacity-70 hover:opacity-100 hover:bg-accent/60 transition-all cursor-pointer"
                  >
                    <XIcon className="size-4" />
                  </button>
                }
              />
              <TooltipContent side="bottom">{t(($) => $.dialog.close)}</TooltipContent>
            </Tooltip>
          </div>
        </div>

        {createdWebhookTrigger ? (
          <WebhookCreatedPanel
            trigger={createdWebhookTrigger}
            onClose={() => {
              setCreatedWebhookTrigger(null);
              onOpenChange(false);
            }}
          />
        ) : (
          <>
        {/* Body: 双栏布局（窄屏经容器级 flex-wrap 纵向堆叠） */}
        <div
          key={contentKey}
          className="flex-1 min-h-0 flex flex-col lg:flex-row overflow-y-auto lg:overflow-hidden"
        >
          {/* 左：runbook（标题 + 提示词） */}
          <div className="flex-none lg:flex-1 min-h-0 min-w-0 flex flex-col border-b lg:border-b-0 lg:border-r">
            <div className="px-6 pt-5 pb-3 shrink-0">
              <TitleEditor
                ref={titleEditorRef}
                autoFocus={isCreate}
                defaultValue={initial.title ?? ""}
                placeholder={t(($) => $.dialog.title_placeholder)}
                className="text-display-sm font-semibold tracking-tight"
                onChange={setTitle}
                onSubmit={handleSubmit}
              />
              {/* role="alert"：标题是 contenteditable，没有可挂 aria-describedby 的
                  input——宣告报错是读屏用户得知 Create 为何毫无反应的唯一途径。 */}
              {showErrors && title.trim().length === 0 && (
                <p role="alert" className="mt-1.5 text-caption text-destructive">
                  {t(($) => $.dialog.error_title_required)}
                </p>
              )}
            </div>

            <div className="px-6 pb-2 shrink-0 flex items-baseline gap-2">
              <span className="text-micro font-semibold tracking-[0.08em] text-muted-foreground uppercase">
                {t(($) => $.dialog.runbook_label)}
              </span>
              <span className="text-caption text-muted-foreground">
                {t(($) => $.dialog.runbook_hint)}
              </span>
            </div>

            <div className="flex-1 min-h-0 px-6 pb-6 flex flex-col lg:h-full">
              <div className="min-h-[200px] lg:min-h-0 lg:h-full overflow-y-auto rounded-lg border border-border bg-background transition-colors focus-within:border-input px-4 py-3">
                <ContentEditor
                  defaultValue={initial.description ?? ""}
                  placeholder={t(($) => $.dialog.description_placeholder)}
                  onUpdate={setDescription}
                  debounceMs={300}
                  showBubbleMenu={false}
                />
              </div>
            </div>
          </div>

          {/* 右：配置 */}
          <aside className="w-full lg:w-[380px] shrink-0 overflow-visible lg:overflow-y-auto px-5 py-5 space-y-5 bg-muted/30">
            <AgentSection
              ref={assigneeTriggerRef}
              selectedType={assigneeType}
              selectedId={assigneeId}
              onChange={handleAssigneeChange}
              selectedName={selectedAssignee?.name}
              selectedDescription={selectedAssignee?.description}
              invalid={showErrors && assigneeId.length === 0}
              errorId={assigneeErrorId}
            />

            <OutputModeSection mode={executionMode} onChange={setExecutionMode} />

            {/* 两种输出模式都显示（MUL-6681）。项目不只是 issue 路由：对 run_only
                autopilot 它是 daemon 唯一可得的项目上下文来源（没有 issue 可继承），
                它决定运行拿到项目的仓库/本地目录——连带 worktree 隔离——还是裸
                workdir。藏起它会令 run_only autopilot 无法绑定项目，且下方的保存曾经
                对 run_only 发 null，静默清掉经 CLI 建立的绑定。 */}
            <ProjectSection
              projectId={projectId}
              selectedProject={selectedProject}
              onChange={setProjectId}
            />

            {executionMode === "create_issue" && (
              <SubscribersSection
                selectedUserIds={subscriberUserIds}
                onChange={setSubscriberUserIds}
              />
            )}

            {isCreate && (
              <TriggerKindSection kind={triggerKind} onChange={setTriggerKind} />
            )}

            {triggerKind === "schedule" ? (
              <div>
                <SectionLabel>{t(($) => $.dialog.section_schedule)}</SectionLabel>
                {showScheduleEmptyState ? (
                  <ScheduleEmptyState onAdd={() => setScheduleAdded(true)} />
                ) : (
                  /* 与详情页的"添加触发"弹窗不同，这里不传 `onValidityChange` /
                     `clearRejection`：底部已没有任何东西再由计划的合法性门控，
                     `scheduleValid` 会没有读者。编辑器仍展示自己的行内拒绝，且
                     `ensureAccepted` 在提交时复问服务器并播报其回答。 */
                  <ScheduleEditor
                    value={schedule}
                    onChange={setSchedule}
                    wsId={wsId}
                    // 保存飞行中锁定：提交路径经网络校验后写它在那次往返前读到的计划，
                    // 中途落进来的编辑会被成功 toast 盖着丢地上。
                    disabled={schedulePillDisabled || submitting}
                    disabledReason={
                      schedulePillDisabled
                        ? t(($) => $.dialog.schedule_disabled_reason)
                        : undefined
                    }
                  />
                )}
              </div>
            ) : (
              <WebhookSection
                isCreate={isCreate}
                eventFilters={eventFilters}
                onEventFiltersChange={setEventFilters}
              />
            )}
          </aside>
        </div>

        {/* 底部 */}
        <div className="flex items-center justify-between gap-3 px-5 py-3 border-t shrink-0 bg-background">
          {/* 计划区在陈述 autopilot 是手动时，提示语退出——直接在那一句下放"自动运行"
              的承诺，正是空态要退休的那句假话。槽位本身保留，否则 justify-between 会
              把按钮挤到左缘。 */}
          <div className="flex items-center gap-1.5 text-caption text-muted-foreground min-w-0">
            {!showScheduleEmptyState && (
              <>
                <Zap className="size-3.5 text-amber-500 shrink-0" />
                <span className="truncate">{t(($) => $.dialog.auto_run_hint)}</span>
              </>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>
              {t(($) => $.dialog.cancel)}
            </Button>
            {/* 只要当前没有正在飞的保存就保持可用——未满足的要求从不把它变暗。置灰
                按钮是条没有理由容身的死路（#6231）；活着的按钮用出错字段上的行内
                报错回应点击，比任何禁用态都说得更多。`handleSubmit` 就是那个闸。 */}
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={submitting}
              aria-busy={submitting || undefined}
            >
              {submitting
                ? isCreate
                  ? t(($) => $.dialog.creating)
                  : t(($) => $.dialog.saving)
                : isCreate
                  ? t(($) => $.dialog.create)
                  : t(($) => $.dialog.save)}
            </Button>
          </div>
        </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// 右列区块
// ---------------------------------------------------------------------------

// 区块小标题；required 仅是有视力用户的预先提示。`role="button"` 不支持
// aria-required，选择器无法携带它；读屏用户获得的替代是"被拦下的提交"的报错——
// 经 aria-describedby 接到触发器、由自身 role="alert" 宣告。
function SectionLabel({
  children,
  required,
}: {
  children: React.ReactNode;
  required?: boolean;
}) {
  return (
    <div className="text-micro font-semibold tracking-[0.08em] text-muted-foreground uppercase mb-2">
      {children}
      {required === true && (
        <span aria-hidden className="ml-0.5 text-destructive">
          *
        </span>
      )}
    </div>
  );
}

// 执行者区块（必填）：AgentPicker + 校验报错。
function AgentSection({
  ref,
  selectedType,
  selectedId,
  onChange,
  selectedName,
  selectedDescription,
  invalid,
  errorId,
}: {
  ref: React.Ref<HTMLButtonElement>;
  selectedType: AutopilotAssigneeType;
  selectedId: string;
  onChange: (next: AssigneeSelection) => void;
  selectedName?: string;
  selectedDescription?: string;
  /** 曾尝试提交但没选执行者。 */
  invalid: boolean;
  errorId: string;
}) {
  const { t } = useT("autopilots");
  const hasSelection = selectedId.length > 0;
  return (
    <div>
      {/* 标为必填，区别于下方项目与订阅者选择器：三者看起来一样，再没别处告诉用户
          只有这个会拦住 Create（#6231）。 */}
      <SectionLabel required>{t(($) => $.dialog.section_assignee)}</SectionLabel>
      <AgentPicker
        assignee={hasSelection ? { type: selectedType, id: selectedId } : null}
        onChange={onChange}
        align="start"
        triggerRender={
          <button
            ref={ref}
            type="button"
            aria-invalid={invalid || undefined}
            aria-describedby={invalid ? errorId : undefined}
            className={cn(
              "w-full flex items-center gap-2.5 rounded-md border bg-background px-3 py-2 text-left",
              "hover:bg-accent/40 transition-colors cursor-pointer",
              invalid && "border-destructive",
            )}
          >
            {hasSelection ? (
              <ActorAvatar
                actorType={selectedType}
                actorId={selectedId}
                size="md"
                showStatusDot={selectedType === "agent"}
              />
            ) : (
              <span className="inline-flex size-7 items-center justify-center rounded-full bg-muted text-muted-foreground">
                <Rocket className="size-3.5" />
              </span>
            )}
            <span className="flex-1 min-w-0">
              <span className="block text-body font-medium truncate">
                {selectedName ?? t(($) => $.dialog.select_assignee)}
              </span>
              {selectedDescription && (
                <span className="block text-caption text-muted-foreground truncate">
                  {selectedDescription}
                </span>
              )}
            </span>
            <ChevronDown className="size-3.5 text-muted-foreground shrink-0" />
          </button>
        }
      />
      {invalid && (
        <p id={errorId} role="alert" className="mt-1.5 text-caption text-destructive">
          {t(($) => $.dialog.error_assignee_required)}
        </p>
      )}
    </div>
  );
}

// 输出模式区块：单选卡片（建 issue / 仅运行）。
function OutputModeSection({
  mode,
  onChange,
}: {
  mode: AutopilotExecutionMode;
  onChange: (mode: AutopilotExecutionMode) => void;
}) {
  const { t } = useT("autopilots");
  return (
    <div>
      <SectionLabel>{t(($) => $.dialog.section_output_mode)}</SectionLabel>
      <div className="space-y-1.5">
        {OUTPUT_MODE_KEYS.map((key) => {
          const selected = key === mode;
          const Icon = OUTPUT_MODE_ICONS[key];
          return (
            <button
              key={key}
              type="button"
              onClick={() => onChange(key)}
              className={cn(
                "w-full flex items-start gap-2.5 rounded-md border px-3 py-2 text-left cursor-pointer transition-colors",
                selected
                  ? "border-primary bg-primary/5"
                  : "bg-background hover:bg-accent/40",
              )}
            >
              <span
                className={cn(
                  "mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full border",
                  selected
                    ? "border-primary bg-primary text-primary-foreground"
                    : "border-muted-foreground/40 bg-background",
                )}
              >
                {selected ? (
                  <Check className="size-2.5" strokeWidth={3} />
                ) : (
                  <Icon className="size-2.5 opacity-0" />
                )}
              </span>
              <span className="flex-1 min-w-0">
                <span className="block text-body font-medium">
                  {t(($) => $.dialog.output_modes[key].label)}
                </span>
                <span className="block text-caption text-muted-foreground">
                  {t(($) => $.dialog.output_modes[key].description)}
                </span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

// 项目区块（可选）：绑定该 autopilot 关联的项目。为什么 run_only 也显示，
// 见上方 JSX 处 MUL-6681 的长注释。
function ProjectSection({
  projectId,
  selectedProject,
  onChange,
}: {
  projectId: string | null;
  selectedProject: { title: string; icon: string | null } | null;
  onChange: (projectId: string | null) => void;
}) {
  const { t } = useT("autopilots");
  return (
    <div>
      <SectionLabel>{t(($) => $.dialog.section_project)}</SectionLabel>
      <p className="mb-2 text-micro text-muted-foreground">
        {t(($) => $.dialog.project_hint)}
      </p>
      <ProjectPicker
        projectId={projectId}
        onUpdate={(updates) => onChange(updates.project_id ?? null)}
        align="start"
        triggerRender={
          <button
            type="button"
            className={cn(
              "w-full flex items-center gap-2.5 rounded-md border bg-background px-3 py-2 text-left",
              "hover:bg-accent/40 transition-colors cursor-pointer",
            )}
          >
            {selectedProject ? (
              <ProjectIcon project={selectedProject} size="md" />
            ) : (
              <span className="inline-flex size-5 items-center justify-center rounded-md bg-muted text-muted-foreground">
                <FolderKanban className="size-3.5" />
              </span>
            )}
            <span className="flex-1 min-w-0 truncate text-body font-medium">
              {selectedProject?.title ?? t(($) => $.dialog.no_project)}
            </span>
            <ChevronDown className="size-3.5 text-muted-foreground shrink-0" />
          </button>
        }
      />
    </div>
  );
}

// 订阅者区块：选择运行结果要通知到哪些成员（仅在 create_issue 输出模式下显示，
// 见下方 `executionMode === "create_issue"` 的条件渲染）。
function SubscribersSection({
  selectedUserIds,
  onChange,
}: {
  selectedUserIds: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useT("autopilots");
  return (
    <div>
      <SectionLabel>{t(($) => $.dialog.section_subscribers)}</SectionLabel>
      <p className="mb-2 text-micro text-muted-foreground">
        {t(($) => $.dialog.subscribers_hint)}
      </p>
      <SubscriberMultiSelect
        selectedIds={selectedUserIds}
        onChange={onChange}
      />
    </div>
  );
}


// 没有计划的 autopilot（手动运行）在本弹窗里的空态卡片：只有点一下"添加"按钮
// 才会真的长出计划编辑器。它要和详情页里触发器的空态保持同一种说法——都是虚线卡片
// 标明"当前手动运行"——否则同一份"没计划"的数据，一个入口显示空态、另一个显示满配的
// 编辑器和"下次运行"预览，用户就会以为已经有计划在跑了。
function ScheduleEmptyState({ onAdd }: { onAdd: () => void }) {
  const { t } = useT("autopilots");
  return (
    <div className="rounded-md border border-dashed p-3 text-center">
      <p className="text-caption text-muted-foreground">
        {t(($) => $.dialog.schedule_empty)}
      </p>
      <Button size="sm" variant="outline" className="mt-2.5" onClick={onAdd}>
        <Plus className="size-3.5 mr-1" />
        {t(($) => $.dialog.schedule_add)}
      </Button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 触发类型分段选择器与 webhook 帮助区块
// ---------------------------------------------------------------------------

// 触发类型选择：仅在创建模式渲染（编辑不支持就地互转类型）。两种可选项——
// schedule（按计划自动跑）与 webhook（等外部调用打进来）。
function TriggerKindSection({
  kind,
  onChange,
}: {
  kind: "schedule" | "webhook";
  onChange: (kind: "schedule" | "webhook") => void;
}) {
  const { t } = useT("autopilots");
  return (
    <div>
      <SectionLabel>{t(($) => $.dialog.section_trigger_kind)}</SectionLabel>
      <SegmentedToggle
        value={kind}
        onChange={onChange}
        buttonClassName="px-3 py-1.5 text-body"
        options={[
          [
            "schedule",
            <span key="schedule" className="flex items-center justify-center gap-1.5">
              <Clock className="h-3.5 w-3.5" />
              {t(($) => $.dialog.trigger_kind_schedule)}
            </span>,
          ],
          [
            "webhook",
            <span key="webhook" className="flex items-center justify-center gap-1.5">
              <Webhook className="h-3.5 w-3.5" />
              {t(($) => $.dialog.trigger_kind_webhook)}
            </span>,
          ],
        ]}
      />
    </div>
  );
}

// webhook 区块：一段解释性帮助文案（创建/编辑时措辞不同）+ 可编辑的事件过滤器。
function WebhookSection({
  isCreate,
  eventFilters,
  onEventFiltersChange,
}: {
  isCreate: boolean;
  eventFilters: WebhookEventFilter[];
  onEventFiltersChange: (filters: WebhookEventFilter[]) => void;
}) {
  const { t } = useT("autopilots");
  return (
    <div className="space-y-3">
      <div>
        <SectionLabel>{t(($) => $.dialog.section_webhook)}</SectionLabel>
        <p className="rounded-md border bg-background px-3 py-2 text-caption text-muted-foreground leading-relaxed">
          {isCreate
            ? t(($) => $.dialog.webhook_help_create)
            : t(($) => $.dialog.webhook_help_edit)}
        </p>
      </div>
      <WebhookEventFilterSection
        filters={eventFilters}
        onChange={onEventFiltersChange}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// webhook 类型创建成功后的收尾面板：就地展示刚铸造好的 URL，用户不必离开弹窗
// 就能复制它（避免"先去找你的 autopilot、点进详情页才能拿 URL"的摩擦）。
// ---------------------------------------------------------------------------

function WebhookCreatedPanel({
  trigger,
  onClose,
}: {
  trigger: AutopilotTrigger;
  onClose: () => void;
}) {
  const { t } = useT("autopilots");

  // 拼 URL 的方式和触发器列表行里用的是同一套：优先采用服务端提供的 webhook_url，
  // 拿不到再退而求其次拼 apiBaseUrl + webhook_path，最后兜底用 origin + path。
  const url =
    buildAutopilotWebhookUrl({
      trigger,
      apiBaseUrl: api.getBaseUrl(),
      currentOrigin: typeof window !== "undefined" ? window.location.origin : undefined,
    }) ?? "";

  return (
    <>
      <div className="flex-1 min-h-0 overflow-y-auto px-8 py-10">
        <div className="mx-auto max-w-xl space-y-5">
          <div className="flex items-center gap-3">
            <span className="inline-flex size-9 items-center justify-center rounded-full bg-primary/15 text-primary">
              <Webhook className="size-4" />
            </span>
            <h2 className="text-title font-semibold tracking-tight">
              {t(($) => $.dialog.webhook_created_title)}
            </h2>
          </div>
          <p className="text-body text-muted-foreground leading-relaxed">
            {t(($) => $.dialog.webhook_created_description)}
          </p>

          <div>
            <div className="text-micro font-semibold tracking-[0.08em] text-muted-foreground uppercase mb-2">
              {t(($) => $.trigger_row.webhook_url_label)}
            </div>
            <WebhookUrlField url={url} size="md" />
          </div>

          <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-caption text-amber-700 dark:text-amber-400 leading-relaxed">
            {t(($) => $.dialog.webhook_created_warning)}
          </div>
        </div>
      </div>

      <div className="flex items-center justify-end gap-3 px-5 py-3 border-t shrink-0 bg-background">
        <Button size="sm" onClick={onClose}>
          {t(($) => $.dialog.webhook_created_done)}
        </Button>
      </div>
    </>
  );
}
