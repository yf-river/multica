// autopilot 详情页：展示单个 autopilot 的全部信息——状态开关、执行者、输出模式、
// 项目、订阅者、描述、触发规则（可增删、webhook 可复制/轮换 URL）、webhook 投递
// 记录、运行历史（可看执行转录），以及"立即运行/编辑/删除"。编辑走共用的
// AutopilotDialog；权限由 can_write/can_manage_access 控制。

"use client";

import { useState } from "react";
import {
  Zap, Play, Clock, Plus, Trash2, CheckCircle2, XCircle, Loader2, Pencil,
  Ban, ChevronDown, ChevronRight,
  Webhook, RotateCw, Server,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { autopilotDetailOptions, autopilotRunsOptions, autopilotRunOptions } from "@multica/core/autopilots/queries";
import { projectDetailOptions } from "@multica/core/projects/queries";
import {
  useUpdateAutopilot,
  useDeleteAutopilot,
  useTriggerAutopilot,
  useCreateAutopilotTrigger,
  useDeleteAutopilotTrigger,
  useRotateAutopilotTriggerWebhookToken,
} from "@multica/core/autopilots/mutations";
import { buildAutopilotWebhookUrl } from "@multica/core/autopilots";
import { api, clientErrorMessage, dispatchReasonCode } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { useNavigation, AppLink } from "../../navigation";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { ActorAvatar } from "../../common/actor-avatar";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { ScheduleEditor } from "./schedule-editor/schedule-editor";
import { WebhookUrlField } from "./webhook-url-field";
import { getDefaultScheduleConfig, type ScheduleConfig } from "./schedule-editor/model";
import { browserTimezone } from "../../common/timezone-select";
import { cronFields, parseCron, toCron } from "./schedule-editor/cron-mapping";
import { useDescribeSchedule } from "./schedule-editor/describe";
import { formatInTimeZone } from "../../common/format-in-time-zone";
import { SegmentedToggle } from "../../common/segmented-toggle";
import { useScheduleSubmitGate } from "./schedule-editor/validate";
import type {
  AutopilotExecutionMode,
  AutopilotRun,
  AutopilotSubscriber,
  AutopilotTrigger,
} from "@multica/core/types";
import type { AgentTask } from "@multica/core/types/agent";
import { ReadonlyContent } from "../../editor";
import { TranscriptButton } from "../../common/task-transcript";
import { AutopilotDialog } from "./autopilot-dialog";
import { runNowToastKind, runNowBlockedKey } from "./run-now-toast";
import { WebhookPayloadPreview } from "./webhook-payload-preview";
import { WebhookDeliveriesSection } from "./webhook-deliveries-section";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useT } from "../../i18n";
import { PageHeader } from "../../layout/page-header";

// 已发生的运行是读者一天里的某个时刻，用读者所在时区显示（不传 timeZone）。尚待
// 发生的运行属于将触发它的那个计划——见触发行，那里传的是触发器自己的时区。
type RunStatus = "issue_created" | "running" | "skipped" | "completed" | "failed";

// 各运行状态的颜色与图标。
const RUN_VISUAL: Record<RunStatus, { color: string; icon: typeof CheckCircle2; spin?: boolean }> = {
  issue_created: { color: "text-blue-500", icon: Clock },
  running: { color: "text-blue-500", icon: Loader2, spin: true },
  // `skipped`（准入检查发现执行者运行时离线，MUL-1899）用弱化色，让它不至于被
  // 读成拉高失败率的项。行上仍显示带跳过上下文的 failure_reason。
  skipped: { color: "text-muted-foreground", icon: Ban },
  completed: { color: "text-emerald-500", icon: CheckCircle2 },
  failed: { color: "text-destructive", icon: XCircle },
};

// WebhookPayloadSlot 在父弹窗真正挂载这个槽位时才懒加载完整运行（含
// trigger_payload）。列表端点为保持响应小而省略 trigger_payload（最坏 256 KiB ×
// N 次运行），所以按需拉详情的请求放在这里。
function WebhookPayloadSlot({ autopilotId, runId }: { autopilotId: string; runId: string }) {
  const wsId = useWorkspaceId();
  const { data, isLoading } = useQuery(
    autopilotRunOptions(wsId, autopilotId, runId),
  );
  if (isLoading) {
    return <Skeleton className="h-9 w-full" />;
  }
  if (!data || data.trigger_payload == null) {
    return null;
  }
  return <WebhookPayloadPreview payload={data.trigger_payload} />;
}

// 运行历史列表中的一行：状态/来源/摘要/时间，行点击跳转；run_only 且有 task_id 的
// 运行提供"查看转录"入口。
function RunRow({ run, agentId, agentName }: { run: AutopilotRun; agentId: string; agentName: string }) {
  const { t, i18n } = useT("autopilots");
  const wsPaths = useWorkspacePaths();
  const status = (RUN_VISUAL[run.status as RunStatus] ? (run.status as RunStatus) : "issue_created");
  const visual = RUN_VISUAL[status];
  const StatusIcon = visual.icon;

  // 对带 task_id 的运行（run_only 模式），拼一个最小 AgentTask，让
  // TranscriptButton 能懒加载执行转录。
  const syntheticTask: AgentTask | null = run.task_id
    ? {
        id: run.task_id,
        agent_id: agentId,
        runtime_id: "",
        issue_id: "",
        status:
          run.status === "running" ? "running" :
          run.status === "completed" ? "completed" :
          run.status === "failed" ? "failed" :
          "queued",
        priority: 0,
        dispatched_at: null,
        started_at: run.triggered_at || null,
        completed_at: run.completed_at || null,
        result: null,
        error: run.failure_reason || null,
        created_at: run.created_at,
      }
    : null;

  const content = (
    <>
      <StatusIcon className={cn("h-4 w-4 shrink-0", visual.color, visual.spin && "animate-spin")} />
      <span className={cn("w-24 shrink-0 text-caption font-medium", visual.color)}>
        {t(($) => $.run_status[status])}
      </span>
      <span className="w-20 shrink-0 text-caption text-muted-foreground">
        {t(($) => $.run_source[run.source as "schedule" | "manual" | "webhook" | "api"]) ?? run.source}
      </span>
      <span className="flex-1 min-w-0 text-caption text-muted-foreground truncate">
        {run.issue_id ? (
          t(($) => $.run.issue_linked)
        ) : run.failure_reason ? (
          <span className="text-destructive">{run.failure_reason}</span>
        ) : null}
      </span>
      <span className="w-32 shrink-0 text-right text-caption text-muted-foreground tabular-nums">
        {formatInTimeZone(run.triggered_at || run.created_at, undefined, i18n.language)}
      </span>
      {syntheticTask && !run.issue_id && (
        <TranscriptButton
          task={syntheticTask}
          agentName={agentName}
          isLive={run.status === "running"}
          title={t(($) => $.run.view_log)}
          headerSlot={
            run.source === "webhook" ? (
              <WebhookPayloadSlot autopilotId={run.autopilot_id} runId={run.id} />
            ) : undefined
          }
        />
      )}
    </>
  );

  const rowClass = "flex items-center gap-3 px-4 py-2.5 text-body hover:bg-accent/30 transition-colors";

  if (run.issue_id) {
    return (
      <AppLink href={wsPaths.issueDetail(run.issue_id)} className={cn(rowClass, "cursor-pointer")}>
        {content}
      </AppLink>
    );
  }

  return <div className={rowClass}>{content}</div>;
}

// 运行历史列表：非 skipped 的运行逐行平铺，skipped 运行收进一个可展开组，避免
// 失败/跳过率高的小问题把列表刷屏。
function RunHistoryList({
  runs,
  agentId,
  agentName,
}: {
  runs: AutopilotRun[];
  agentId: string;
  agentName: string;
}) {
  const visibleRuns = runs.filter((run) => run.status !== "skipped");
  const skippedRuns = runs.filter((run) => run.status === "skipped");

  return (
    <div className="rounded-md border overflow-hidden">
      {visibleRuns.map((run) => (
        <RunRow key={run.id} run={run} agentId={agentId} agentName={agentName} />
      ))}
      {skippedRuns.length > 0 && (
        <SkippedRunsGroup runs={skippedRuns} agentId={agentId} agentName={agentName} />
      )}
    </div>
  );
}

// 折叠的"被跳过运行"组：头部一行摘要（次数 + 最近时间），展开后逐行列出。
function SkippedRunsGroup({
  runs,
  agentId,
  agentName,
}: {
  runs: AutopilotRun[];
  agentId: string;
  agentName: string;
}) {
  const { t, i18n } = useT("autopilots");
  const [open, setOpen] = useState(false);
  const latestRun = runs[0];
  const ToggleIcon = open ? ChevronDown : ChevronRight;

  return (
    <div className="border-t bg-muted/20">
      <button
        type="button"
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-body hover:bg-accent/30 transition-colors"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
      >
        <ToggleIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
        <Ban className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="w-24 shrink-0 text-caption font-medium text-muted-foreground">
          {t(($) => $.run.skipped_group.label)}
        </span>
        <span className="flex-1 min-w-0 text-caption text-muted-foreground truncate">
          {t(($) => $.run.skipped_group.summary, { count: runs.length })}
        </span>
        {latestRun && (
          <span className="w-32 shrink-0 text-right text-caption text-muted-foreground tabular-nums">
            {formatInTimeZone(latestRun.triggered_at || latestRun.created_at, undefined, i18n.language)}
          </span>
        )}
      </button>
      {open && (
        <div className="border-t bg-background">
          {runs.map((run) => (
            <RunRow key={run.id} run={run} agentId={agentId} agentName={agentName} />
          ))}
        </div>
      )}
    </div>
  );
}

// 触发行：展示一个触发规则（定时/API/webhook）的可读信息与操作。定时器行有白话
// 回读 + cron + 下次运行；webhook 行额外展示/复制/轮换/删除 URL。
function TriggerRow({ trigger, autopilotId, canWrite }: { trigger: AutopilotTrigger; autopilotId: string; canWrite: boolean }) {
  const { t, i18n } = useT("autopilots");
  const describeSchedule = useDescribeSchedule();
  const deleteTrigger = useDeleteAutopilotTrigger();
  const rotateToken = useRotateAutopilotTriggerWebhookToken();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  // 删除触发器。
  const handleDelete = async () => {
    setDeleting(true);
    try {
      await deleteTrigger.mutateAsync({ autopilotId, triggerId: trigger.id });
      toast.success(t(($) => $.trigger_row.toast_deleted));
      setConfirmOpen(false);
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.trigger_row.toast_delete_failed),
      );
    } finally {
      setDeleting(false);
    }
  };

  const isWebhook = trigger.kind === "webhook";
  const isApi = trigger.kind === "api";
  // 优先从服务器给的 webhook_url 解析 URL，否则用 API 基址（桌面）或
  // window.origin（web）拼接。都不可用时回退到相对路径。
  const webhookUrl = isWebhook
    ? buildAutopilotWebhookUrl({
        trigger,
        apiBaseUrl: api.getBaseUrl(),
        currentOrigin: typeof window !== "undefined" ? window.location.origin : undefined,
      })
    : null;

  // 轮换 webhook token。
  const handleRotate = async () => {
    try {
      await rotateToken.mutateAsync({ autopilotId, triggerId: trigger.id });
      toast.success(t(($) => $.trigger_row.toast_rotated));
      setRotateOpen(false);
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.trigger_row.toast_rotate_failed),
      );
    }
  };

  const Icon = isWebhook ? Webhook : isApi ? Zap : Clock;
  const showWebhookUrlRow = isWebhook && webhookUrl;
  // 表达式超出结构化模型时为 null——那些行单独继续显示原始 cron。
  const scheduleConfig = trigger.cron_expression
    ? parseCron(trigger.cron_expression, trigger.timezone ?? "UTC")
    : null;
  const scheduleDescription = scheduleConfig ? describeSchedule(scheduleConfig) : null;

  // 删除控件单独提出，让 webhook 触发器可把它内联渲染在 URL 操作行（与其他操作
  // 按钮同排），而定时/API 触发器——没有 URL 行——仍把它钉在行右上角。否则外层
  // flex 用 `items-start` 时垃圾桶图标会视觉悬浮在 URL 操作按钮上方。
  const deleteButton = canWrite ? (
    <Button
      size="icon"
      variant="ghost"
      className="h-7 w-7 shrink-0"
      onClick={() => setConfirmOpen(true)}
      title={t(($) => $.trigger_row.delete_dialog.confirm)}
    >
      <Trash2 className="h-3.5 w-3.5 text-muted-foreground" />
    </Button>
  ) : null;

  return (
    <div className="flex items-start gap-3 rounded-md border px-3 py-2">
      <Icon className="h-4 w-4 shrink-0 text-muted-foreground mt-0.5" />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-body font-medium">{t(($) => $.trigger_kind[trigger.kind])}</span>
          {trigger.label && (
            <span className="text-caption text-muted-foreground">({trigger.label})</span>
          )}
          {!trigger.enabled && (
            <span className="text-caption bg-muted px-1.5 py-0.5 rounded">
              {t(($) => $.trigger_row.disabled_badge)}
            </span>
          )}
          {isApi && (
            <span className="text-caption bg-muted px-1.5 py-0.5 rounded">
              {t(($) => $.trigger_row.deprecated_badge)}
            </span>
          )}
        </div>
        {trigger.cron_expression && (
          // 白话句子领衔；原始表达式降到次要行，两者不会混成一坨。
          <div className="mt-0.5 space-y-0.5">
            <div className="text-caption text-muted-foreground">
              {scheduleDescription ?? trigger.cron_expression}
              {trigger.timezone && ` (${trigger.timezone})`}
            </div>
            {scheduleDescription !== null && scheduleConfig !== null && (
              // 只显示五个字段：时区已在上面的句子里读出来了，人能看懂——与编辑器
              // 回读的规则一致。
              <div className="font-mono text-micro text-muted-foreground">
                {cronFields(scheduleConfig)}
              </div>
            )}
          </div>
        )}
        {trigger.next_run_at && (
          <div className="text-caption text-muted-foreground">
            {t(($) => $.trigger_row.next_label, {
              date: formatInTimeZone(
                trigger.next_run_at,
                trigger.timezone ?? undefined,
                i18n.language,
              ),
            })}
          </div>
        )}
        {showWebhookUrlRow && (
          <div className="mt-1.5">
            <WebhookUrlField
              url={webhookUrl}
              actions={
                <>
                  {canWrite && (
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-7 w-7 shrink-0"
                      onClick={() => setRotateOpen(true)}
                      title={t(($) => $.trigger_row.rotate_url)}
                      disabled={rotateToken.isPending}
                    >
                      <RotateCw className={cn("h-3.5 w-3.5 text-muted-foreground", rotateToken.isPending && "animate-spin")} />
                    </Button>
                  )}
                  {deleteButton}
                </>
              }
            />
          </div>
        )}
      </div>
      {!showWebhookUrlRow && deleteButton}
      <AlertDialog open={confirmOpen} onOpenChange={(v) => { if (!v && !deleting) setConfirmOpen(false); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.trigger_row.delete_dialog.title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.trigger_row.delete_dialog.description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>
              {t(($) => $.trigger_row.delete_dialog.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              disabled={deleting}
              className="bg-destructive text-white hover:bg-destructive/90"
            >
              {deleting
                ? t(($) => $.trigger_row.delete_dialog.deleting)
                : t(($) => $.trigger_row.delete_dialog.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={rotateOpen} onOpenChange={(v) => { if (!v && !rotateToken.isPending) setRotateOpen(false); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.trigger_row.rotate_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.trigger_row.rotate_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={rotateToken.isPending}>
              {t(($) => $.trigger_row.rotate_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleRotate} disabled={rotateToken.isPending}>
              {rotateToken.isPending
                ? t(($) => $.trigger_row.rotate_in_progress)
                : t(($) => $.trigger_row.rotate_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// "添加触发"弹窗：选择类型（定时 / webhook）。定时类型内嵌 ScheduleEditor 编辑
// cron 计划，提交前经提交闸向服务器确认；webhook 类型提交后生成带 token 的 URL。
function AddTriggerDialog({
  open,
  onOpenChange,
  autopilotId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  autopilotId: string;
}) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();
  const createTrigger = useCreateAutopilotTrigger();
  const [kind, setKind] = useState<"schedule" | "webhook">("schedule");
  const [config, setConfig] = useState<ScheduleConfig>(() =>
    getDefaultScheduleConfig(browserTimezone()),
  );
  const [label, setLabel] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const scheduleGate = useScheduleSubmitGate(wsId);
  const canSubmit = !submitting && (kind !== "schedule" || scheduleGate.scheduleValid);

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      if (kind === "schedule") {
        if (!(await scheduleGate.ensureAccepted(config))) {
          setSubmitting(false);
          return;
        }
        const cronExpr = toCron(config);
        if (!cronExpr.trim()) {
          setSubmitting(false);
          return;
        }
        await createTrigger.mutateAsync({
          autopilotId,
          kind: "schedule",
          cron_expression: cronExpr,
          timezone: config.timezone || undefined,
          label: label.trim() || undefined,
        });
        toast.success(t(($) => $.add_trigger_dialog.toast_added_schedule));
      } else {
        await createTrigger.mutateAsync({
          autopilotId,
          kind: "webhook",
          label: label.trim() || undefined,
        });
        toast.success(t(($) => $.add_trigger_dialog.toast_added_webhook));
      }
      onOpenChange(false);
      setKind("schedule");
      setConfig(getDefaultScheduleConfig(browserTimezone()));
      setLabel("");
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.add_trigger_dialog.toast_add_failed),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-sm">
        <DialogTitle>{t(($) => $.add_trigger_dialog.title)}</DialogTitle>
        {/* DialogContent 是网格布局，没有 min-w-0 时此项的最小宽度就是内容宽——而 cron
            回读是行内不可断的一长串，会把轨道推出弹窗而不是截断。 */}
        <div className="min-w-0 space-y-4 pt-2">
          <div>
            <label className="text-caption font-medium text-muted-foreground">
              {t(($) => $.add_trigger_dialog.type_label)}
            </label>
            <div className="mt-1">
              <SegmentedToggle
                value={kind}
                onChange={setKind}
                buttonClassName="px-3 py-1.5 text-body"
                options={[
                  [
                    "schedule",
                    <span key="schedule" className="flex items-center justify-center gap-1.5">
                      <Clock className="h-3.5 w-3.5" />
                      {t(($) => $.add_trigger_dialog.type_schedule)}
                    </span>,
                  ],
                  [
                    "webhook",
                    <span key="webhook" className="flex items-center justify-center gap-1.5">
                      <Webhook className="h-3.5 w-3.5" />
                      {t(($) => $.add_trigger_dialog.type_webhook)}
                    </span>,
                  ],
                ]}
              />
            </div>
          </div>

          {kind === "schedule" ? (
            <ScheduleEditor
              value={config}
              onChange={(next) => {
                scheduleGate.clearRejection();
                setConfig(next);
              }}
              wsId={wsId}
              onValidityChange={scheduleGate.onValidityChange}
              // 与 autopilot 弹窗同理：提交路径读取计划、经网络校验、再写入所读内容
              // ——落在那个窗口内的编辑会被丢弃。
              disabled={submitting}
            />
          ) : (
            <p className="rounded-md bg-muted/50 px-3 py-2 text-caption text-muted-foreground">
              {t(($) => $.add_trigger_dialog.webhook_help)}
            </p>
          )}

          <div>
            <label className="text-caption font-medium text-muted-foreground">
              {t(($) => $.add_trigger_dialog.label_field)}
            </label>
            <input
              type="text"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={t(($) => $.add_trigger_dialog.label_placeholder)}
              className="mt-1 w-full rounded-md border bg-background px-3 py-2 text-body outline-none focus:ring-1 focus:ring-ring"
            />
          </div>
          <div className="flex justify-end pt-1">
            <Button size="sm" onClick={handleSubmit} disabled={!canSubmit}>
              {submitting
                ? t(($) => $.add_trigger_dialog.submitting)
                : t(($) => $.add_trigger_dialog.submit)}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// 只读订阅者 chip 行；编辑经由 AutopilotDialog → SubscriberMultiSelect，所以详情页
// 从不持有飞行中的选择状态。
function SubscriberChips({
  subscribers,
}: {
  subscribers: AutopilotSubscriber[] | undefined;
}) {
  const { t } = useT("autopilots");
  const { getActorName } = useActorName();
  const members = (subscribers ?? []).filter((s) => s.user_type === "member");
  if (members.length === 0) {
    return (
      <div className="mt-1 text-body text-muted-foreground">
        {t(($) => $.detail.field_subscribers_none)}
      </div>
    );
  }
  return (
    <div className="mt-1 flex flex-wrap gap-1.5">
      {members.map((s) => (
        <span
          key={`${s.user_type}:${s.user_id}`}
          className="inline-flex items-center gap-1 rounded-full border bg-background px-2 py-0.5 text-caption"
        >
          <ActorAvatar actorType="member" actorId={s.user_id} size="xs" />
          <span className="max-w-[14rem] truncate">
            {getActorName("member", s.user_id)}
          </span>
        </span>
      ))}
    </div>
  );
}

/**
 * autopilot 详情页。加载详情后按区块展示：属性（执行者/创建者/模式/项目/订阅者/
 * 描述）、触发规则（增删、webhook URL 复制与轮换）、webhook 投递记录、运行历史，
 * 头部提供启停开关与"立即运行/编辑"，底部危险区可删除。数据与操作权限跟随
 * can_write。
 * @param autopilotId autopilot 的 ID（路由参数）。
 */
export function AutopilotDetailPage({ autopilotId }: { autopilotId: string }) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const router = useNavigation();
  const { getActorName } = useActorName();

  const { data, isLoading } = useQuery(autopilotDetailOptions(wsId, autopilotId));
  const { data: runs = [], isLoading: runsLoading } = useQuery(autopilotRunsOptions(wsId, autopilotId));
  const updateAutopilot = useUpdateAutopilot();
  const deleteAutopilot = useDeleteAutopilot();
  const triggerAutopilot = useTriggerAutopilot();
  const projectId = data?.autopilot.project_id ?? null;
  const { data: project, isLoading: projectLoading } = useQuery({
    ...projectDetailOptions(wsId, projectId ?? ""),
    enabled: Boolean(projectId),
  });

  const [triggerDialogOpen, setTriggerDialogOpen] = useState(false);
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  if (isLoading) {
    return (
      <div className="flex h-full flex-col">
        <PageHeader>
          <Skeleton className="h-4 w-4" />
          <span className="text-muted-foreground">/</span>
          <Skeleton className="h-4 w-32" />
        </PageHeader>
        <div className="flex-1 overflow-y-auto">
          <div className="max-w-4xl mx-auto p-6 space-y-8">
            <section className="space-y-4">
              <Skeleton className="h-3 w-20" />
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-1">
                  <Skeleton className="h-3 w-12" />
                  <Skeleton className="h-5 w-32" />
                </div>
                <div className="space-y-1">
                  <Skeleton className="h-3 w-12" />
                  <Skeleton className="h-5 w-24" />
                </div>
              </div>
            </section>
            <section className="space-y-3">
              <Skeleton className="h-4 w-16" />
              <Skeleton className="h-10 w-full rounded-md" />
            </section>
            <section className="space-y-3">
              <Skeleton className="h-4 w-24" />
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </section>
          </div>
        </div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground">
        {t(($) => $.detail.not_found)}
      </div>
    );
  }

  const { autopilot, triggers } = data;
  const collaborators = data.collaborators ?? [];
  // 把缺失的 can_write（旧服务器）当作"允许"——后端才是真正的闸，UI 只在服务器
  // 明确说调用方不能写时才隐藏控件。
  const canWrite = autopilot.can_write !== false;
  // 管理访问列表比"能写"更窄：被授权的协作者可编辑/运行，但不能授予/撤销。服务器
  // 不发该字段时（旧后端）回退到 canWrite。
  const canManageAccess = autopilot.can_manage_access ?? canWrite;

  // 手动"立即运行"。
  const handleRunNow = async () => {
    try {
      const run = await triggerAutopilot.mutateAsync(autopilotId);
      // 手动"立即运行"即使准入挡住了也会返回 200，所以 toast 由运行的领域状态驱动，
      // 而非 HTTP 2xx（MUL-4525）。成功是白名单（issue_created/running）——skipped
      // 警告、failed 或未知/未来状态报错——绝不误报 "triggered"。
      const kind = runNowToastKind(run?.status);
      if (kind === "success") {
        toast.success(t(($) => $.detail.toast_triggered));
        return;
      }
      // reason_code 是服务器准入时决定的稳定类型化原因；未知/缺失 code 降级为通用
      // "未触发"文案。
      const message = t(($) => $.detail[runNowBlockedKey(run?.reason_code)]);
      if (kind === "warning") {
        toast.warning(message);
      } else {
        toast.error(message);
      }
    } catch (e: any) {
      const reason = dispatchReasonCode(e);
      if (reason) {
        toast.error(t(($) => $.detail[runNowBlockedKey(reason)]));
        return;
      }
      // 只有 4xx 的文案写给用户；5xx 是内部服务器细节（MUL-6472），所以无法归类的
      // 派发失败显示本地化的通用句子而不是原始 body。
      toast.error(clientErrorMessage(e) || t(($) => $.detail.toast_trigger_failed));
    }
  };

  // 删除 autopilot，成功后退回列表页。
  const handleDelete = async () => {
    setDeleting(true);
    try {
      await deleteAutopilot.mutateAsync(autopilotId);
      toast.success(t(($) => $.detail.toast_deleted));
      router.push(wsPaths.autopilots());
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.detail.toast_delete_failed),
      );
      setDeleting(false);
    }
  };

  const handleToggleStatus = (checked: boolean) => {
    updateAutopilot.mutate({ id: autopilotId, status: checked ? "active" : "paused" });
  };

  return (
    <div className="flex h-full flex-col">
      {/* 头部（面包屑）：标题 + 启停开关 + 状态 + 编辑/立即运行 */}
      <BreadcrumbHeader
        segments={[{ href: wsPaths.autopilots(), label: t(($) => $.page.title) }]}
        leaf={
          <>
            <h1 className="min-w-0 truncate text-body font-medium text-foreground">{autopilot.title}</h1>
            <div className="ml-1 flex items-center gap-1.5 shrink-0">
              <Switch
                size="sm"
                checked={autopilot.status === "active"}
                onCheckedChange={handleToggleStatus}
                disabled={autopilot.status === "archived"}
                aria-label={
                  autopilot.status === "active"
                    ? t(($) => $.detail.pause_aria)
                    : t(($) => $.detail.activate_aria)
                }
              />
              <span className={cn(
                "text-caption font-medium hidden sm:inline",
                autopilot.status === "active" ? "text-emerald-500" :
                autopilot.status === "paused" ? "text-amber-500" :
                "text-muted-foreground",
              )}>
                {t(($) => $.status[autopilot.status])}
              </span>
            </div>
          </>
        }
        actions={
          canWrite ? (
            <>
              <Button size="sm" variant="outline" onClick={() => setEditDialogOpen(true)} className="px-2 sm:px-2.5" aria-label={t(($) => $.detail.edit)}>
                <Pencil className="h-3.5 w-3.5 sm:mr-1" />
                <span className="hidden sm:inline">{t(($) => $.detail.edit)}</span>
              </Button>
              <Button
                size="sm"
                onClick={handleRunNow}
                disabled={autopilot.status !== "active" || triggerAutopilot.isPending}
                className="px-2 sm:px-2.5"
                aria-label={triggerAutopilot.isPending ? t(($) => $.detail.running) : t(($) => $.detail.run_now)}
              >
                {triggerAutopilot.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 sm:mr-1 animate-spin" />
                ) : (
                  <Play className="h-3.5 w-3.5 sm:mr-1" />
                )}
                <span className="hidden sm:inline">
                  {triggerAutopilot.isPending
                    ? t(($) => $.detail.running)
                    : t(($) => $.detail.run_now)}
                </span>
              </Button>
            </>
          ) : null
        }
      />

      {autopilot.pause_reason === "agent_runtime_required" && (
        // 横幅：因执行者未绑定运行时而暂停，引导去绑定。
        <div className="flex shrink-0 items-center gap-2 border-b border-amber-500/30 bg-amber-500/10 px-6 py-2 text-caption text-amber-900 dark:text-amber-100">
          <Server className="size-3.5 shrink-0" />
          <span className="flex-1">
            {t(($) => $.detail.paused_runtime_required)}
          </span>
          {autopilot.assignee_type === "agent" && (
            <AppLink
              href={`${wsPaths.agentDetail(autopilot.assignee_id)}?view=general`}
              className="font-medium underline underline-offset-2"
            >
              {t(($) => $.detail.bind_runtime)}
            </AppLink>
          )}
        </div>
      )}

      <div className="flex-1 overflow-y-auto">
        <div className="max-w-4xl mx-auto p-6 space-y-8">
          {/* 属性 */}
          <section className="space-y-4">
            <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">
              {t(($) => $.detail.section_properties)}
            </h2>
            <div className="grid grid-cols-2 gap-4 text-body">
              <div>
                <label className="text-caption text-muted-foreground">{t(($) => $.detail.field_agent)}</label>
                <div className="mt-1 flex items-center gap-2">
                  <ActorAvatar
                    actorType={autopilot.assignee_type}
                    actorId={autopilot.assignee_id}
                    size="sm"
                    enableHoverCard={autopilot.assignee_type === "agent"}
                    showStatusDot={autopilot.assignee_type === "agent"}
                  />
                  <span className="cursor-pointer">
                    {getActorName(autopilot.assignee_type, autopilot.assignee_id)}
                  </span>
                </div>
              </div>
              <div>
                <label className="text-caption text-muted-foreground">{t(($) => $.detail.field_created_by)}</label>
                <div className="mt-1 flex items-center gap-2">
                  {/* 创建者可能是成员或智能体：HTTP 创建路径今天盖的是成员，但后端
                      逻辑也会写 created_by_type=agent。ActorAvatar/getActorName 两者
                      都解析，所以绝不假定是 member。 */}
                  <ActorAvatar
                    actorType={autopilot.created_by_type}
                    actorId={autopilot.created_by_id}
                    size="sm"
                    enableHoverCard
                    showStatusDot={autopilot.created_by_type === "agent"}
                  />
                  <span className="cursor-pointer">
                    {getActorName(autopilot.created_by_type, autopilot.created_by_id)}
                  </span>
                </div>
              </div>
              <div>
                <label className="text-caption text-muted-foreground">{t(($) => $.detail.field_output_mode)}</label>
                <div className="mt-1">
                  {t(($) => $.execution_mode[autopilot.execution_mode as AutopilotExecutionMode])}
                </div>
              </div>
              {/* 两种输出模式都显示（MUL-6681）：run_only 型 autopilot 的项目决定
                  它的执行环境（仓库 / 本地目录，进而决定 worktree 隔离），所以调试某
                  次运行的运维人员需要在这里看到它。 */}
              <div>
                <label className="text-caption text-muted-foreground">{t(($) => $.detail.field_project)}</label>
                <div className="mt-1 min-w-0">
                  {!autopilot.project_id ? (
                    <span className="text-muted-foreground">{t(($) => $.detail.no_project)}</span>
                  ) : projectLoading ? (
                    <Skeleton className="h-5 w-32" />
                  ) : project ? (
                    <AppLink
                      href={wsPaths.projectDetail(project.id)}
                      className="inline-flex max-w-full items-center gap-1.5 text-foreground hover:underline"
                    >
                      <ProjectIcon project={project} size="md" />
                      <span className="truncate">{project.title}</span>
                    </AppLink>
                  ) : (
                    <span className="text-muted-foreground">{t(($) => $.detail.project_unavailable)}</span>
                  )}
                </div>
              </div>
              {autopilot.execution_mode === "create_issue" && (
                <div className="col-span-2">
                  <label className="text-caption text-muted-foreground">
                    {t(($) => $.detail.field_subscribers)}
                  </label>
                  <SubscriberChips
                    subscribers={autopilot.subscribers}
                  />
                </div>
              )}
              {autopilot.description && (
                <div className="col-span-2">
                  <label className="text-caption text-muted-foreground">{t(($) => $.detail.field_prompt)}</label>
                  <div className="mt-1">
                    <ReadonlyContent content={autopilot.description} />
                  </div>
                </div>
              )}
            </div>
          </section>

          {/* 触发规则 */}
          <section className="space-y-3">
            <div className="flex items-center justify-between">
              <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">
                {t(($) => $.detail.section_triggers)}
              </h2>
              {canWrite && (
                <Button size="sm" variant="outline" onClick={() => setTriggerDialogOpen(true)}>
                  <Plus className="h-3.5 w-3.5 mr-1" />
                  {t(($) => $.detail.add_trigger)}
                </Button>
              )}
            </div>
            {triggers.length === 0 ? (
              <div className="rounded-md border border-dashed p-4 text-center text-body text-muted-foreground">
                {t(($) => $.detail.no_triggers)}
              </div>
            ) : (
              <div className="space-y-2">
                {triggers.map((trig) => (
                  <TriggerRow key={trig.id} trigger={trig} autopilotId={autopilotId} canWrite={canWrite} />
                ))}
              </div>
            )}
          </section>

          {/* Webhook 投递记录——仅在配了至少一个 webhook 触发时渲染。组件自己拉取，
              让仅定时型 autopilot 不用为一次空列表查询买单。 */}
          <WebhookDeliveriesSection
            autopilotId={autopilotId}
            hasWebhookTrigger={triggers.some((trig) => trig.kind === "webhook")}
          />

          {/* 运行历史 */}
          <section className="space-y-3">
            <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">
              {t(($) => $.detail.section_run_history)}
            </h2>
            {runsLoading ? (
              <div className="space-y-1">
                {Array.from({ length: 3 }).map((_, i) => (
                  <Skeleton key={i} className="h-10 w-full" />
                ))}
              </div>
            ) : runs.length === 0 ? (
              <div className="rounded-md border border-dashed p-4 text-center text-body text-muted-foreground">
                {t(($) => $.detail.no_runs)}
              </div>
            ) : (
              <RunHistoryList
                runs={runs}
                agentId={autopilot.assignee_id}
                agentName={getActorName(autopilot.assignee_type, autopilot.assignee_id)}
              />
            )}
          </section>

          {/* 危险区：删除 */}
          {canWrite && (
            <section className="space-y-3 pt-4 border-t">
              <h2 className="text-body font-medium text-destructive uppercase tracking-wider">
                {t(($) => $.detail.section_danger)}
              </h2>
              <Button size="sm" variant="destructive" onClick={() => setDeleteConfirmOpen(true)}>
                <Trash2 className="h-3.5 w-3.5 mr-1" />
                {t(($) => $.detail.delete_button)}
              </Button>
            </section>
          )}
        </div>
      </div>

      {/* 只在打开时挂载，与编辑弹窗同理：否则被拒的 cron 会把 scheduleValid=false
          残留到下一次打开。 */}
      {triggerDialogOpen && (
        <AddTriggerDialog
          open={triggerDialogOpen}
          onOpenChange={setTriggerDialogOpen}
          autopilotId={autopilotId}
        />
      )}
      {editDialogOpen && (
        <AutopilotDialog
          mode="edit"
          open={editDialogOpen}
          onOpenChange={setEditDialogOpen}
          autopilotId={autopilot.id}
          initial={{
            title: autopilot.title,
            description: autopilot.description ?? "",
            project_id: autopilot.project_id ?? null,
            assignee_type: autopilot.assignee_type,
            assignee_id: autopilot.assignee_id,
            execution_mode: autopilot.execution_mode as AutopilotExecutionMode,
            subscriber_user_ids:
              autopilot.subscribers
                ?.filter((s) => s.user_type === "member")
                .map((s) => s.user_id) ?? [],
          }}
          triggers={triggers}
          collaborators={collaborators}
          canManageAccess={canManageAccess}
        />
      )}
      <AlertDialog
        open={deleteConfirmOpen}
        onOpenChange={(v) => { if (!v && !deleting) setDeleteConfirmOpen(false); }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.detail.delete_dialog.title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.detail.delete_dialog.description, { title: autopilot.title })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>
              {t(($) => $.detail.delete_dialog.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              disabled={deleting}
              className="bg-destructive text-white hover:bg-destructive/90"
            >
              {deleting
                ? t(($) => $.detail.delete_dialog.deleting)
                : t(($) => $.detail.delete_dialog.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
