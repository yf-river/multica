"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Loader2, Zap } from "lucide-react";
import { toast } from "sonner";
import { dispatchReasonCode } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { quickActionListOptions, useRunQuickAction } from "@multica/core/quick-actions";
import type { Comment, CommentTriggerOutcome, QuickAction } from "@multica/core/types";
import { QUICK_ACTION_SIDEBAR_LIMIT } from "@multica/core/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";

// 本文件：QuickActionsSection —— 侧栏"快捷操作"区段（MUL-5465）。
//
// 列表*不*按 invoke 权限过滤。早期版本会藏起观看者跑不了的操作，结果是
// 同一条问题上两个人看到不同侧栏、却没有任何东西解释差异——比一个会告诉你
// 为什么被拒的按钮更难排查。权限只由运行接口裁定一次；被拒则打开对话框。
//
// 交互只有一种：点击即运行。没有逐操作的输入字段——`/` 斜杠命令已经会把
// 渲染好的操作投进 composer 供编辑，比单个固定字段更灵活地覆盖了
// "同一操作、细节不同"的场景，所以第二套 UI 被删了。
//
// 结果 toast 刻意做到"按结局说事"，而不是笼统的"完成"。对着一个在本问题上
// 已有待办任务的 agent 再点一次，*不会*启动第二次运行——DB 每条
// (issue, agent) 只允许一个待办任务，新评论会被并入既有那条。把它报成
// 成功，是说一个用户之后还得花时间排查的谎。

export function QuickActionsSection({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  // 用可空读取而非 useWorkspaceId()：本区段是侧栏上的增强件，
  // 在非工作区路由下应降级为"不渲染"，而不是抛错把整个 issue detail 带崩。
  const wsId = useCurrentWorkspace()?.id ?? "";
  const [open, setOpen] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [blocked, setBlocked] = useState<QuickAction | null>(null);

  const { data = [], isLoading } = useQuery({
    ...quickActionListOptions(wsId),
    enabled: wsId !== "",
  });
  const actions = useMemo(() => data.filter((a) => a.status === "active"), [data]);

  // 什么都没配置就什么都不渲染：侧栏里一个空的区段标题纯属噪音。
  if (isLoading || actions.length === 0) return null;

  const visible = showAll ? actions : actions.slice(0, QUICK_ACTION_SIDEBAR_LIMIT);
  const hiddenCount = actions.length - visible.length;

  return (
    <div>
      <button
        type="button"
        className={cn(
          "mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70",
          !open && "text-muted-foreground hover:text-foreground",
        )}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.detail.section_quick_actions)}
        <ChevronRight
          className={cn(
            "!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform",
            open && "rotate-90",
          )}
        />
      </button>
      {open ? (
        <div className="space-y-0.5 pl-2">
          {visible.map((action) => (
            <QuickActionRow
              key={action.id}
              action={action}
              issueId={issueId}
              onBlocked={() => setBlocked(action)}
            />
          ))}
          {hiddenCount > 0 ? (
            <button
              type="button"
              className="w-full rounded-md px-2 py-1 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/70 hover:text-foreground"
              onClick={() => setShowAll(true)}
            >
              {t(($) => $.detail.quick_actions_more, { count: hiddenCount })}
            </button>
          ) : null}
        </div>
      ) : null}

      {/* 每一种被拒都只给一条消息。读这条弹窗的人与能修绑定的人不是同一个——
          无论哪条路，点击者的下一步都一样；诊断细节放在设置里，
          让所有者去处理。 */}
      <AlertDialog open={blocked !== null} onOpenChange={(v) => !v && setBlocked(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.detail.quick_action_blocked_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.detail.quick_action_blocked_body, { name: blocked?.name ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogAction>{t(($) => $.detail.quick_action_blocked_ok)}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * 把每个目标的结果翻译成一句诚实的话。
 *
 * `coalesced` 是那个关键项：点击被接受，但没有*新*运行开始。
 * "已并入 Lambda 当前这次运行"是事实；"Lambda 开工了"不是。
 */
function outcomeMessage(
  outcome: CommentTriggerOutcome | undefined,
  targetName: string,
  t: ReturnType<typeof useT<"issues">>["t"],
): { message: string; kind: "success" | "info" | "error" } {
  if (!outcome) {
    // 完全没有 outcome 意味着评论存下了、但什么都没报——
    // 用中性的方式呈现，而不是宣称启动了一次运行。
    return { message: t(($) => $.detail.quick_action_posted), kind: "info" };
  }
  switch (outcome.status) {
    case "queued":
      return { message: t(($) => $.detail.quick_action_queued, { name: targetName }), kind: "success" };
    case "coalesced":
      return { message: t(($) => $.detail.quick_action_coalesced, { name: targetName }), kind: "info" };
    case "deferred":
      return { message: t(($) => $.detail.quick_action_deferred, { name: targetName }), kind: "info" };
    case "blocked":
      return { message: t(($) => $.detail.quick_action_blocked, { name: targetName }), kind: "error" };
    default:
      // 服务端驱动的枚举：未知状态绝不能静默渲染成成功。
      return { message: t(($) => $.detail.quick_action_posted), kind: "info" };
  }
}

/**
 * 单条快捷操作行：点击运行，运行中禁用并显示 spinner。
 */
function QuickActionRow({
  action,
  issueId,
  onBlocked,
}: {
  action: QuickAction;
  issueId: string;
  onBlocked: () => void;
}) {
  const { t } = useT("issues");
  const runMutation = useRunQuickAction(issueId);
  const running = runMutation.isPending;

  const targetName = action.target_name || t(($) => $.detail.quick_action_target_fallback);

  const handleRun = async () => {
    try {
      const comment: Comment = await runMutation.mutateAsync({ quickActionId: action.id });
      const { message, kind } = outcomeMessage(comment.trigger_outcomes?.[0], targetName, t);
      if (kind === "success") toast.success(message);
      else if (kind === "error") toast.error(message);
      else toast.info(message);
    } catch (error) {
      // 权限被拒是用户需要被解释的结构化 403，不是该重试的瞬时故障——
      // 它走弹窗。其它一律保持 toast。
      if (dispatchReasonCode(error) === "invocation_not_allowed") {
        onBlocked();
        return;
      }
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  const row = (
    <div>
      <button
        type="button"
        disabled={running}
        onClick={() => void handleRun()}
        className={cn(
          "group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-body transition-colors",
          "hover:bg-accent/70 disabled:cursor-default disabled:opacity-60",
        )}
      >
        {running ? (
          <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
        ) : (
          <Zap className="size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1 truncate">{action.name}</span>
        {action.target_name ? (
          <ActorAvatar
            actorType={action.assignee_type === "squad" ? "squad" : "agent"}
            actorId={action.assignee_id}
            size="xs"
            className="shrink-0"
            showStatusDot={action.assignee_type === "agent"}
          />
        ) : null}
      </button>
    </div>
  );

  return (
    <Tooltip>
      <TooltipTrigger render={row} />
      <TooltipContent side="left">
        <div className="space-y-0.5">
          <div className="font-medium">{action.name}</div>
          {action.description ? <div>{action.description}</div> : null}
          <div className="text-muted-foreground">
            {t(($) => $.detail.quick_action_runs_as, { name: targetName })}
          </div>
        </div>
      </TooltipContent>
    </Tooltip>
  );
}
