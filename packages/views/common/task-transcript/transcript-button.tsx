// 转录按钮：一个紧凑的图标按钮，点击打开"任务执行实录"完整弹窗（AgentTranscriptDialog）。
// 用在任何列智能体任务的地方（Issue 活动卡、智能体详情活动页）。按钮自己管理弹窗
// 开关与数据的懒加载，父级只需把它放进去。支持三种数据来源：父级直接给时间线、
// 订阅实时任务消息缓存、终态任务点击时一次性拉取。

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Loader2, ScrollText } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { api } from "@multica/core/api";
import {
  chatKeys,
  isTaskMessageTaskId,
  taskMessagesOptions,
} from "@multica/core/chat/queries";
import type { AgentTask } from "@multica/core/types/agent";
import type { TaskMessagePayload } from "@multica/core/types/events";
import { CodeDiffCard } from "./code-diff-card";
import { AgentTranscriptDialog } from "./agent-transcript-dialog";
import { buildTimeline, type TimelineItem } from "./build-timeline";

interface TranscriptButtonProps {
  task: AgentTask;
  agentName: string;
  /**
   * 预加载好的时间线。提供时按钮跳过拉取、点开立即显示——供那些自己已在累积
   * 时间线的界面使用。终态任务省略此项：按钮会在首次点击时用
   * `api.listTaskMessages` 拉取并缓存结果。实时任务也要省略：按钮会订阅共享的
   * task-messages 缓存，让弹窗内容随新事件到来持续增长。
   */
  items?: TimelineItem[];
  isLive?: boolean;
  className?: string;
  title?: string;
  renderButton?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /**
   * 渲染在转录事件列表上方的可选内容。用于把 autopilot 的 webhook 载荷
   * 与运行历史并排展示。
   */
  headerSlot?: React.ReactNode;
}

/**
 * 打开完整转录弹窗的紧凑图标按钮。用在任何列出智能体任务的界面（Issue 活动卡、
 * 智能体详情的活动标签页）。自己管理弹窗状态与懒加载——父级只管把它放下。
 *
 * 三种数据模式：
 *  - 父级给 items：时间线由父级持有，这里只负责渲染。
 *  - 实时缓存：`isLive` 为真、未给 items 且任务 ID 符合实时格式——订阅共享的
 *    `["task-messages", taskId]` 缓存（由 WS `task:message` 流灌数据），打开的
 *    弹窗随事件实时增长；打开时还会强制做一次按 seq 合并的补拉，弥合 WS 重连
 *    可能造成的空洞。
 *  - 懒加载：终态任务在首次点击时拉取一次并做本地缓存。
 * @param task 智能体任务。
 * @param agentName 智能体名称（用于弹窗标题等）。
 * @param items 预加载时间线（见类型注释）。
 * @param isLive 任务是否仍实时运行中。
 * @param className 附加样式。
 * @param title 按钮的提示/可访问名称。
 * @param renderButton 是否渲染按钮本体（false 时只挂弹窗逻辑）。
 * @param open 受控的弹窗开关。
 * @param onOpenChange 弹窗开关变化回调。
 * @param headerSlot 弹窗内容上方的可选插槽。
 */
export function TranscriptButton({
  task,
  agentName,
  items: providedItems,
  isLive = false,
  className,
  title = "View transcript",
  renderButton = true,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  headerSlot,
}: TranscriptButtonProps) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadedItems, setLoadedItems] = useState<TimelineItem[] | null>(null);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = controlledOnOpenChange ?? setUncontrolledOpen;

  // 实时缓存模式：运行中的任务在持续喂共享的 task-messages 缓存，所以直接渲染
  // 该缓存，而不是一次性拉取的本地快照。
  const liveCacheMode =
    isLive && providedItems === undefined && isTaskMessageTaskId(task.id);

  // 在一次打开会话期间"锁存"实时路径。任务一结束父级就把 `isLive` 翻成 false；
  // 没有锁存的话，弹窗会在用户查看中途退化成空的 `loadedItems`。一直停留在缓存
  // 路径上能保证每条已下发的 seq 都留在屏上，并让弹窗在"运行中 → 终态"切换时做
  // 最后一次权威补拉。
  const [liveSession, setLiveSession] = useState(false);
  useEffect(() => {
    if (!open) {
      setLiveSession(false);
      return;
    }
    if (liveCacheMode) {
      setLiveSession(true);
    }
  }, [liveCacheMode, open]);

  // 实时模式渲染缓存；懒加载/父级提供模式渲染本地 state。
  const items = providedItems ?? loadedItems ?? [];

  const handleClick = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (liveCacheMode) {
        setLiveSession(true);
        setOpen(true);
        return;
      }
      if (providedItems !== undefined || loadedItems !== null) {
        setOpen(true);
        return;
      }
      // 都没有 → 首次点击才拉取，拉完缓存并打开弹窗。
      setLoading(true);
      api
        .listTaskMessages(task.id)
        .then((msgs) => {
          setLoadedItems(buildTimeline(msgs));
          setOpen(true);
        })
        .catch((err) => {
          console.error(err);
          setLoadedItems([]);
          setOpen(true);
        })
        .finally(() => setLoading(false));
    },
    [liveCacheMode, providedItems, loadedItems, setOpen, task.id],
  );

  // 弹窗打开期间监听全局导航事件，发生路由跳转就收起弹窗。
  useEffect(() => {
    if (!open) return;

    const handleGlobalNavigate = () => {
      setOpen(false);
    };

    window.addEventListener("multica:navigate", handleGlobalNavigate);
    return () => {
      window.removeEventListener("multica:navigate", handleGlobalNavigate);
    };
  }, [open, setOpen]);

  return (
    <>
      {renderButton ? (
        <Tooltip>
          <TooltipTrigger
            render={<button type="button" />}
            onClick={handleClick}
            disabled={loading}
            aria-label={title}
            className={cn(
              "flex items-center justify-center rounded p-1 text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors disabled:opacity-50",
              className,
            )}
          >
            {loading ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <ScrollText className="h-3.5 w-3.5" />
            )}
          </TooltipTrigger>
          <TooltipContent>{title}</TooltipContent>
        </Tooltip>
      ) : null}

      {open &&
        (liveSession ? (
          <LiveTranscriptDialog
            task={task}
            agentName={agentName}
            isLive={isLive}
            onOpenChange={setOpen}
            headerSlot={<>{headerSlot}<CodeDiffCard result={task.result} /></>}
          />
        ) : (
          <AgentTranscriptDialog
            open={open}
            onOpenChange={setOpen}
            task={task}
            items={items}
            agentName={agentName}
            isLive={isLive}
            headerSlot={<>{headerSlot}<CodeDiffCard result={task.result} /></>}
          />
        ))}
    </>
  );
}

interface LiveTranscriptDialogProps {
  task: AgentTask;
  agentName: string;
  isLive: boolean;
  onOpenChange: (open: boolean) => void;
  headerSlot?: React.ReactNode;
}

/**
 * 基于共享 task-messages 缓存的实时转录视图。只在弹窗打开期间挂载，所以关闭状态
 * 的行不持有任何查询订阅，不会抬高基线请求量。
 *
 * 缓存观察是只读的（`enabled: false`）：WS `task:message` 处理器才是实时写入方，
 * 这里的补拉是唯一的 fetch。刻意不让 React Query 自行 refetch——它的结果会无脑
 * 整体替换缓存，可能丢掉飞行中途到达的某条 seq；而补拉是按 seq 合并的，两边都
 * 不会丢消息。
 */
function LiveTranscriptDialog({
  task,
  agentName,
  isLive,
  onOpenChange,
  headerSlot,
}: LiveTranscriptDialogProps) {
  const queryClient = useQueryClient();
  const { data } = useQuery({
    ...taskMessagesOptions(task.id),
    enabled: false,
  });

  // 打开时强制补拉一次，任务进入终态时再补一次。`taskMessagesOptions` 是
  // `staleTime: Infinity`，所以单纯的订阅永远不会 refetch——WS 重连的空洞
  // （或已完成 Issue 任务不会重播的尾部消息）不补拉就永远缺着。查询的
  // `structuralSharing` 会把这次响应并入实时流已经写好的缓存，两边都不丢 seq。
  useEffect(() => {
    if (!isTaskMessageTaskId(task.id)) return;
    let cancelled = false;
    api
      .listTaskMessages(task.id)
      .then((msgs) => {
        if (cancelled) return;
        queryClient.setQueryData<TaskMessagePayload[]>(
          chatKeys.taskMessages(task.id),
          msgs,
        );
      })
      .catch((err) => {
        console.error(err);
      });
    return () => {
      cancelled = true;
    };
  }, [task.id, isLive, queryClient]);

  const items = useMemo(() => buildTimeline(data ?? []), [data]);

  return (
    <AgentTranscriptDialog
      open
      onOpenChange={onOpenChange}
      task={task}
      items={items}
      agentName={agentName}
      isLive={isLive}
      headerSlot={headerSlot}
    />
  );
}
