"use client";

// 本文件是 Agent 列表里每一行的“操作菜单”（行尾的 ⋯ 下拉）：根据 Agent 生命周期
// （正常/已归档）和当前用户权限，动态给出“新标签页打开 / 全部停任务 / 复制为模板 /
// 恢复 / 归档”等动作，其中停任务和归档都带二次确认弹窗。

import { useState } from "react";
import {
  AlertCircle,
  Copy,
  ExternalLink,
  MoreHorizontal,
  RotateCcw,
  Square,
  Trash2,
} from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import type { AgentPresenceDetail } from "@multica/core/agents";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace/queries";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useT } from "../../i18n";
import { AppLink, useIntentNavigate } from "../../navigation";

interface AgentRowActionsProps {
  /** 本行对应的 Agent。 */
  agent: Agent;
  /** 该 Agent 的实时在场详情（正在跑/排队中的任务数），用于判断是否显示“停止”动作。 */
  presence: AgentPresenceDetail | null | undefined;
  // 当前用户能否管理这个 Agent（是 Agent 的拥有者，或是工作区管理员/拥有者）。
  // 与后端的 canManageAgent 校验保持一致——服务端仍是最终裁决，这里只是把用户
  // 没权限执行的操作从界面上藏起来。
  canManage: boolean;
  // “复制为模板”跳转的目标：手动创建表单页，并用本 Agent 的配置预填好模板。
  // 用 href 而不是回调，是为了让菜单项成为真正的链接（按住修饰键点击可新开标签页）。
  duplicateHref: string;
}

/**
 * Agent 列表行级的下拉操作菜单。显示哪些动作，取决于 (a) Agent 的生命周期状态
 * （正常 vs 已归档）和 (b) 调用者的权限等级。“新标签页打开”对每一行都适用，
 * 所以触发器始终渲染。
 *
 * 行本身是普通 `<div>`，整行跳转靠鼠标 `onClick`（见 `useRowLink`），而不是包一层
 * `<a>`。宿主单元格会阻止事件冒泡，所以打开本菜单不会误触发行跳转；菜单和弹窗
 * 内容都通过 portal 渲染到行结构之外，也不受影响。
 */
export function AgentRowActions({
  agent,
  presence,
  canManage,
  duplicateHref,
}: AgentRowActionsProps) {
  const { t } = useT("agents");
  const { t: tCommon } = useT("common");
  const paths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const [confirmArchive, setConfirmArchive] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);

  const isArchived = !!agent.archived_at;
  const runningCount = presence?.runningCount ?? 0;
  const queuedCount = presence?.queuedCount ?? 0;
  const hasActiveWork = runningCount + queuedCount > 0;

  // 这里一次性推导出菜单里该显示哪些项，让下面 JSX 保持一长串扁平的条件渲染，
  // 而不是嵌套一堆“角色/状态”的分支判断。
  const showStop = canManage && !isArchived && hasActiveWork;
  const showDuplicate = !isArchived; // 任何工作区成员都能复制
  // 系统内置 Agent 不允许归档——服务端会拒绝，且工作区的入口本来就跑在其中一个上面。
  // 与其让它点下去弹失败 toast，不如直接不显示这个动作。
  const isSystemAgent = !!agent.system_key;
  const showArchive = canManage && !isArchived && !isSystemAgent;
  const showRestore = canManage && isArchived;

  const invalidateAgents = () => {
    qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
  };

  const handleArchive = async () => {
    try {
      await api.archiveAgent(agent.id);
      invalidateAgents();
      toast.success(t(($) => $.row_actions.agent_archived_toast));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.row_actions.archive_failed_toast));
    }
  };

  const handleRestore = async () => {
    try {
      await api.restoreAgent(agent.id);
      invalidateAgents();
      toast.success(t(($) => $.row_actions.agent_restored_toast));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.row_actions.restore_failed_toast));
    }
  };

  const handleCancelTasks = async () => {
    try {
      const { cancelled } = await api.cancelAgentTasks(agent.id);
      invalidateAgents();
      toast.success(
        cancelled === 0
          ? t(($) => $.row_actions.no_tasks_to_cancel_toast)
          : t(($) => $.row_actions.cancelled_tasks_toast, { count: cancelled }),
      );
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.row_actions.cancel_failed_toast));
    }
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              aria-label={t(($) => $.row.actions_aria)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-100 @2xl:opacity-0 transition-opacity hover:bg-accent hover:text-accent-foreground group-hover/row:opacity-100 data-popup-open:bg-accent data-popup-open:opacity-100 data-popup-open:text-accent-foreground"
            >
              <MoreHorizontal className="size-4" />
            </button>
          }
        />
        <DropdownMenuContent align="end" className="w-auto">
          {/* 和所有其他实体行菜单同构：显式 CTA 通过导航适配器以前台标签页打开
              （如果用锚点式菜单项，就得指望原生点击能在菜单关闭前活下来）。 */}
          <DropdownMenuItem
            onClick={() =>
              intentNavigate(
                paths.agentDetail(agent.id),
                "foreground-tab",
              )
            }
          >
            <ExternalLink className="h-3.5 w-3.5" />
            {tCommon(($) => $.navigation.open_in_new_tab)}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          {showStop && (
            <DropdownMenuItem
              onClick={() => setConfirmCancel(true)}
            >
              <Square className="h-3.5 w-3.5" />
              {t(($) => $.row_actions.cancel_all_tasks)}
            </DropdownMenuItem>
          )}
          {showDuplicate && (
            <DropdownMenuItem render={<AppLink href={duplicateHref} />}>
              <Copy className="h-3.5 w-3.5" />
              {t(($) => $.row_actions.duplicate)}
            </DropdownMenuItem>
          )}
          {showRestore && (
            <DropdownMenuItem onClick={handleRestore}>
              <RotateCcw className="h-3.5 w-3.5" />
              {t(($) => $.row_actions.restore)}
            </DropdownMenuItem>
          )}
          {showArchive && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setConfirmArchive(true)}
              >
                <Trash2 className="h-3.5 w-3.5" />
                {t(($) => $.row_actions.archive)}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {confirmCancel && (
        <AlertDialog
          open
          onOpenChange={(v) => {
            if (!v) setConfirmCancel(false);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t(($) => $.row_actions.cancel_dialog_title, { name: agent.name })}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {describeCancelImpact(runningCount, queuedCount, t)}
                {runningCount > 0 && t(($) => $.row_actions.cancel_dialog_running_note)}
                {t(($) => $.row_actions.cancel_dialog_irreversible)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t(($) => $.row_actions.cancel_dialog_keep)}</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => {
                  setConfirmCancel(false);
                  void handleCancelTasks();
                }}
              >
                {t(($) => $.row_actions.cancel_dialog_confirm)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}

      {confirmArchive && (
        <AlertDialog
          open
          onOpenChange={(v) => {
            if (!v) setConfirmArchive(false);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <div className="flex items-start gap-3">
                <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-destructive/10">
                  <AlertCircle className="h-5 w-5 text-destructive" />
                </div>
                <div className="flex-1">
                  <AlertDialogTitle>
                    {t(($) => $.row_actions.archive_dialog_title, { name: agent.name })}
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    {t(($) => $.row_actions.archive_dialog_description)}
                  </AlertDialogDescription>
                </div>
              </div>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t(($) => $.row_actions.archive_dialog_cancel)}</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => {
                  setConfirmArchive(false);
                  void handleArchive();
                }}
              >
                {t(($) => $.row_actions.archive_dialog_confirm)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </>
  );
}

type AgentsT = ReturnType<typeof useT<"agents">>["t"];

/** 拼出“停止全部任务”确认弹窗里的影响描述：无任务 / 有正在跑或排队任务两种文案。 */
function describeCancelImpact(running: number, queued: number, t: AgentsT): string {
  if (running === 0 && queued === 0) {
    return t(($) => $.row_actions.cancel_dialog_no_tasks);
  }
  const parts: string[] = [];
  if (running > 0) parts.push(t(($) => $.row_actions.cancel_dialog_running, { count: running }));
  if (queued > 0) parts.push(t(($) => $.row_actions.cancel_dialog_queued, { count: queued }));
  return t(($) => $.row_actions.cancel_dialog_impact, {
    summary: parts.join(" + "),
    count: running + queued,
  });
}
