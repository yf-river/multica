// 聊天会话列表：IM 风格的行（头像 + 标题 + agent 名 + 最后消息预览 + 时间 +
// 红色未读角标），支持置顶/归档/停止/删除，内置"历史 / 归档"两个视图。
"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  ChevronLeft,
  ChevronRight,
  Clock,
  Loader2,
  Pin,
  PinOff,
  Square,
  Trash2,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useWorkspaceId } from "@multica/core/hooks";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { useWorkspacePresenceMap } from "@multica/core/agents";
import { api } from "@multica/core/api";
import { pendingChatTasksOptions, chatKeys, sortChatSessions } from "@multica/core/chat/queries";
import {
  useDeleteChatSession,
  useSetChatSessionArchived,
  useSetChatSessionPinned,
} from "@multica/core/chat/mutations";
import { useChatStore } from "@multica/core/chat";
import type { Agent, ChatSession, PendingChatTasksResponse } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import {
  RowActionsMenu,
  handleRowActivationKey,
  type RowActionItem,
} from "../../common/row-actions-menu";
import { resolveClickIntent, useOptionalNavigation } from "../../navigation";
import { createLogger } from "@multica/core/logger";
import { removeChatMessageFromCaches } from "@multica/core/realtime";
import { useLocale, useT } from "../../i18n";

const apiLogger = createLogger("chat.api");

// IM 风格时间戳：今天 → 时刻，今年 → M/D，更早 → 完整日期。
function formatChatTime(dateStr: string, locale: string): string {
  const d = new Date(dateStr);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
  }
  if (d.getFullYear() === now.getFullYear()) {
    return d.toLocaleDateString(locale, { month: "numeric", day: "numeric" });
  }
  return d.toLocaleDateString(locale);
}

// 把（可能是 markdown / 多行的）消息压成一行预览。
function toPreview(content: string): string {
  return content
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#*`>~]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

/**
 * IM 风格会话列表：每行是 agent 头像 + 会话标题 + agent 名 + 最后消息预览 + 时间，
 * 带红色未读*数量*徽标。运行中的 agent 显示"typing…"指示；最后一次回复失败则
 * 显示破坏性提示。行按服务端顺序渲染（最近活跃优先）。重命名在对话头部的 ⋯ 菜单里，
 * 不在这里。
 *
 * 两个视图，本地切换：默认的"历史"视图列出活跃会话，悬停一行显示置顶 + 归档
 * （运行中则显示停止）——归档是可逆的一键默认操作，不会误删任何东西。页脚入口
 * （"已归档 · N"）切到"归档"视图，它列出已归档会话，而且是唯一能彻底删除
 * 会话的地方（悬停 → 取消归档 + 删除）。两个视图都读同一条扁平会话缓存，
 * 在本地按 `status` 切分。
 */
export function ChatThreadList({
  sessions,
  agents,
  activeSessionId,
  onSelectSession,
  onArchive,
}: {
  sessions: ChatSession[];
  agents: Agent[];
  activeSessionId: string | null;
  onSelectSession: (session: ChatSession) => void;
  // 归档由父组件拥有，让"选中推进"保持布局感知（桌面推进到下一聊天；移动端
  // 退回列表），并走共享控制器——见 ChatPage.handleArchive。
  onArchive: (session: ChatSession) => void;
}) {
  const { t } = useT("chat");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  // 空安全的 slug（不用会抛错的 useWorkspacePaths）：列表在测试里会渲染在
  // 工作区路由之外；没有 slug 时，"按住修饰键点击在新标签页打开"的入口
  // 直接保持关闭。
  const slug = useWorkspaceSlug();
  const sessionHref = (sessionId: string) =>
    slug ? `${paths.workspace(slug).chat()}?session=${sessionId}` : null;
  // 列表可能在工作区路由外渲染（测试）；那种情况下浏览器标签入口直接保持关闭。
  const navigation = useOptionalNavigation();
  const getShareableUrl = navigation?.getShareableUrl;
  const agentById = useMemo(() => new Map(agents.map((a) => [a.id, a])), [agents]);

  // 在本地切分扁平缓存：活跃会话填默认历史视图，归档会话填"归档"视图。
  // 两边都先按置顶排序（再按活跃度），让列表在一次乐观置顶/归档或 WS 补丁
  // 原地改动扁平缓存后仍保持有序。
  const historySessions = useMemo(
    () => sortChatSessions(sessions.filter((s) => s.status !== "archived")),
    [sessions],
  );
  const archivedSessions = useMemo(
    () => sortChatSessions(sessions.filter((s) => s.status === "archived")),
    [sessions],
  );

  // 当前显示哪个视图。归档列表清空（最后一个被取消归档 / 删除）时回落到历史，
  // 绝不把用户丢在一个空归档页上。
  const [view, setView] = useState<"history" | "archived">("history");
  useEffect(() => {
    if (view === "archived" && archivedSessions.length === 0) setView("history");
  }, [view, archivedSessions.length]);

  const [confirmingDeleteId, setConfirmingDeleteId] = useState<string | null>(null);
  const [confirmingStopId, setConfirmingStopId] = useState<string | null>(null);
  const [stoppingTaskId, setStoppingTaskId] = useState<string | null>(null);
  const deleteSession = useDeleteChatSession();
  const setPinned = useSetChatSessionPinned();
  const setArchived = useSetChatSessionArchived();
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const queryClient = useQueryClient();

  const { data: pending } = useQuery(pendingChatTasksOptions(wsId));
  const pendingTaskBySessionId = useMemo(
    () => new Map((pending?.tasks ?? []).map((task) => [task.chat_session_id, task])),
    [pending],
  );

  // 每个 agent 的在线状态：OFFLINE agent 上的 pending 任务显示"等待中"，
  // 而不是误导性的"typing…"（任务要等 agent 回来才会跑）。和对话面板状态
  // pill 用同一个在线来源，保持两个界面一致。
  const presence = useWorkspacePresenceMap(wsId);

  useEffect(() => {
    if (!confirmingStopId || pendingTaskBySessionId.has(confirmingStopId)) return;
    setConfirmingStopId(null);
  }, [confirmingStopId, pendingTaskBySessionId]);

  const handleConfirmDelete = (session: ChatSession) => {
    const sessionId = session.id;
    if (activeSessionId === sessionId) setActiveSession(null);
    deleteSession.mutate(sessionId, {
      onSettled: () => setConfirmingDeleteId(null),
    });
  };

  const handleConfirmStop = (
    session: ChatSession,
    task: PendingChatTasksResponse["tasks"][number],
  ) => {
    setStoppingTaskId(task.task_id);
    queryClient.setQueryData<PendingChatTasksResponse>(chatKeys.pendingTasks(wsId), (current) => {
      if (!current) return current;
      return {
        ...current,
        tasks: current.tasks.filter((item) => item.task_id !== task.task_id),
      };
    });
    queryClient.setQueryData(chatKeys.pendingTask(session.id), {});
    queryClient.invalidateQueries({ queryKey: chatKeys.messages(session.id) });
    queryClient.invalidateQueries({ queryKey: chatKeys.messagesPage(session.id) });

    api.cancelTaskById(task.task_id).then(
      (result) => {
        const restored = result.cancelled_chat_message;
        if (restored?.restore_to_input) {
          removeChatMessageFromCaches(queryClient, restored.chat_session_id, restored.message_id);
        }
        apiLogger.info("cancelTask.success (list row)", { taskId: task.task_id, sessionId: session.id });
      },
      (err) =>
        apiLogger.warn("cancelTask.error (list row; task may have already finished)", {
          taskId: task.task_id,
          sessionId: session.id,
          err,
        }),
    ).finally(() => {
      queryClient.invalidateQueries({ queryKey: chatKeys.pendingTasks(wsId) });
      queryClient.invalidateQueries({ queryKey: chatKeys.pendingTask(session.id) });
      setStoppingTaskId(null);
      setConfirmingStopId(null);
    });
  };

  const renderRow = (session: ChatSession) => {
    const isCurrent = session.id === activeSessionId;
    const agent = agentById.get(session.agent_id) ?? null;
    const agentName = agent?.name.trim() || null;
    const pendingTask = pendingTaskBySessionId.get(session.id);
    const isRunning = !!pendingTask;
    // 只有"offline"（确实长期离线）才把 typing 降级成 waiting。
    // 未知/加载中的在线状态保留乐观的"typing…"，绝不因为在线数据还没到就压掉它。
    const agentOffline = agent
      ? presence.byAgent.get(agent.id)?.availability === "offline"
      : false;
    const unread = isCurrent ? 0 : (session.unread_count ?? 0);
    const isConfirmingDelete = confirmingDeleteId === session.id;
    const isConfirmingStop = confirmingStopId === session.id && !!pendingTask;
    const isConfirmingAction = isConfirmingDelete || isConfirmingStop;
    const titleText = session.title?.trim() || t(($) => $.window.untitled);
    const last = session.last_message ?? null;
    const timeText = last
      ? formatChatTime(last.created_at, locale)
      : formatChatTime(session.updated_at, locale);

    // 第二行：typing/waiting → failed → 预览。
    let previewNode: React.ReactNode;
    if (isRunning && agentOffline) {
      // 任务在排队但 agent 离线——agent 回来后才会跑。显示静态的"等待中"，
      // 而不是动画的"typing"。
      previewNode = (
        <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
          <Clock className="size-3 shrink-0" />
          <span className="truncate">{t(($) => $.list.waiting)}</span>
        </span>
      );
    } else if (isRunning) {
      previewNode = (
        <span className="flex min-w-0 items-center gap-1.5 text-emerald-500">
          <Loader2 className="size-3 shrink-0 animate-spin" />
          <span className="truncate">{t(($) => $.list.typing)}</span>
        </span>
      );
    } else if (last?.failure_reason) {
      previewNode = <span className="block truncate text-destructive">{t(($) => $.list.failed)}</span>;
    } else if (last?.message_kind === "no_response") {
      // no_response 回合把一段非空的英文兜底存成内容，所以即使旧客户端预览也
      // 不会空白；新客户端显示本地化斜体提示，而不是那段兜底文字（MUL-4351）。
      previewNode = (
        <span className="block truncate italic text-muted-foreground">
          {t(($) => $.list.no_response_preview)}
        </span>
      );
    } else if (last) {
      previewNode = (
        <span className={cn("block truncate", unread > 0 ? "text-foreground" : "text-muted-foreground")}>
          {last.role === "user" ? t(($) => $.list.you_prefix) : ""}
          {toPreview(last.content)}
        </span>
      );
    } else {
      previewNode = <span className="block truncate text-muted-foreground">{t(($) => $.list.no_messages)}</span>;
    }

    // 一份动作列表同时驱动两个操作面——无悬停的紧凑菜单与带悬停的悬停条——
    // 所以它们不会漂移。硬删除只存在于归档视图；历史视图提供可逆的归档。
    const rowActions: RowActionItem[] =
      view === "archived"
        ? [
            {
              key: "unarchive",
              icon: <ArchiveRestore className="size-3.5" />,
              label: t(($) => $.list.unarchive),
              onSelect: () =>
                setArchived.mutate({ sessionId: session.id, archived: false }),
            },
            {
              key: "delete",
              icon: <Trash2 className="size-3.5" />,
              label: t(($) => $.session_history.row_delete_aria),
              danger: true,
              onSelect: () => setConfirmingDeleteId(session.id),
            },
          ]
        : [
            {
              key: "pin",
              icon: session.pinned ? (
                <PinOff className="size-3.5" />
              ) : (
                <Pin className="size-3.5 -rotate-45" />
              ),
              label: session.pinned
                ? t(($) => $.list.unpin)
                : t(($) => $.list.pin),
              onSelect: () =>
                setPinned.mutate({ sessionId: session.id, pinned: !session.pinned }),
            },
            isRunning
              ? {
                  key: "stop",
                  icon: <Square className="size-3 fill-current" />,
                  label: t(($) => $.session_history.row_stop_aria),
                  danger: true,
                  onSelect: () => setConfirmingStopId(session.id),
                }
              : {
                  key: "archive",
                  icon: <Archive className="size-3.5" />,
                  label: t(($) => $.list.archive),
                  onSelect: () => onArchive(session),
                },
          ];

    return (
      <div
        key={session.id}
        aria-current={isCurrent ? "true" : undefined}
        tabIndex={0}
        onClick={(e) => {
          if (isConfirmingAction || e.defaultPrevented) return;
          // 普通点击保持主从式选中。修饰键点击则把会话开成独立的浏览器标签。
          const href = sessionHref(session.id);
          if (
            href &&
            getShareableUrl &&
            resolveClickIntent(e) !== "push"
          ) {
            window.open(
              getShareableUrl(href),
              "_blank",
              "noopener,noreferrer",
            );
            return;
          }
          onSelectSession(session);
        }}
        onAuxClick={(e) => {
          if (isConfirmingAction || e.defaultPrevented || e.button !== 1) return;
          const href = sessionHref(session.id);
          if (!href || !getShareableUrl) return;
          e.preventDefault();
          window.open(getShareableUrl(href), "_blank", "noopener,noreferrer");
        }}
        onKeyDown={(e) => {
          if (isConfirmingAction) return;
          handleRowActivationKey(e, () => onSelectSession(session));
        }}
        className={cn(
          // 固定行高，让任何东西（悬停动作、确认提示）都改不了行尺寸、
          // 不会让列表跳动。内容垂直居中。
          "group/row relative flex h-14 min-w-0 cursor-default items-center gap-3 rounded-md px-2 outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ring",
          isCurrent ? "bg-accent" : "hover:bg-accent/50",
          isConfirmingAction && "bg-destructive/5 hover:bg-destructive/5",
        )}
      >
        {/* 细圆环让照片头像和兜底头像读起来是同一个圆
            （兜底头像模糊的底色否则看起来更小）。 */}
        {agent ? (
          <ActorAvatar actorType="agent" actorId={agent.id} size="lg" enableHoverCard className="ring-1 ring-inset ring-border" />
        ) : (
          <span className="size-8 shrink-0" />
        )}

        <div className="min-w-0 flex-1">
          {/* 第 1 行：名字 + 时间（时间保持原位；悬停动作浮层在下方） */}
          <div className="flex items-center gap-1.5">
            {session.pinned && (
              <Pin
                aria-label={t(($) => $.list.pinned)}
                className="size-3 shrink-0 -rotate-45 fill-current text-muted-foreground"
              />
            )}
            <span className={cn("min-w-0 flex-1 truncate text-body", unread > 0 ? "font-semibold text-foreground" : "font-medium")}>
              {titleText}
            </span>
            <span className="ml-auto shrink-0 text-micro text-muted-foreground">{timeText}</span>
          </div>

          {/* 第 2 行：预览 + 未读徽标，或一条行内确认提示 */}
          <div className="mt-0.5 flex items-center gap-2">
            {isConfirmingDelete ? (
                <ConfirmRow
                  label={t(($) => $.session_history.delete_dialog.title)}
                  cancelText={t(($) => $.session_history.delete_dialog.cancel)}
                  confirmText={
                    deleteSession.isPending
                      ? t(($) => $.session_history.delete_dialog.confirming)
                      : t(($) => $.session_history.delete_dialog.confirm)
                  }
                  pending={deleteSession.isPending}
                  onCancel={() => setConfirmingDeleteId(null)}
                  onConfirm={() => handleConfirmDelete(session)}
                />
              ) : isConfirmingStop && pendingTask ? (
                <ConfirmRow
                  label={t(($) => $.session_history.stop_dialog.title)}
                  cancelText={t(($) => $.session_history.stop_dialog.cancel)}
                  confirmText={
                    stoppingTaskId === pendingTask.task_id
                      ? t(($) => $.session_history.stop_dialog.confirming)
                      : t(($) => $.session_history.stop_dialog.confirm)
                  }
                  pending={stoppingTaskId === pendingTask.task_id}
                  onCancel={() => setConfirmingStopId(null)}
                  onConfirm={() => handleConfirmStop(session, pendingTask)}
                />
              ) : (
                <>
                  <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden text-caption">
                    {agentName && (
                      <>
                        <span className="max-w-[40%] shrink-0 truncate font-medium text-muted-foreground">
                          {agentName}
                        </span>
                        <span aria-hidden="true" className="shrink-0 text-faint-foreground">
                          ·
                        </span>
                      </>
                    )}
                    <div className="min-w-0 flex-1 overflow-hidden">{previewNode}</div>
                  </div>
                  {unread > 0 && (
                    <span
                      aria-label={t(($) => $.session_history.row_subtitle.new_reply)}
                      // 比醒目的 `destructive` token 更柔和的暖红——
                      // 是 IM 未读徽标，不是错误。
                      className="inline-flex h-[18px] min-w-[18px] shrink-0 items-center justify-center rounded-full bg-[oklch(0.62_0.14_18)] px-1 text-micro font-semibold text-white"
                    >
                      {unread > 99 ? "99+" : unread}
                    </span>
                  )}
                </>
              )}
            </div>
        </div>

        {/* 紧凑动作菜单——悬停条在触屏上的对应物，没有悬停能力的指针永远够不到它。
            它占真实布局空间（而不是盖在预览上），并在可悬停指针下让位给悬停条。 */}
        {!isConfirmingAction && (
          <RowActionsMenu
            label={t(($) => $.list.row_actions_aria)}
            groups={[rowActions]}
          />
        )}

        {/* 悬停动作——绝对定位，显隐不会改变行高（那曾让列表跳动）。
            键盘聚焦也会亮出它们，没有鼠标也能触达。 */}
        {!isConfirmingAction && (
          <div className="absolute inset-y-0 right-1 hidden items-center gap-0.5 rounded-md bg-gradient-to-l from-accent from-40% to-transparent pl-10 pr-1 [@media(hover:hover)]:group-hover/row:flex [@media(hover:hover)]:group-focus-within/row:flex">
            {rowActions.map((action) => (
              <RowAction
                key={action.key}
                icon={action.icon}
                label={action.label}
                danger={action.danger}
                onClick={action.onSelect}
              />
            ))}
          </div>
        )}
      </div>
    );
  };

  // 归档视图：一个返回头部，然后是归档行。删除只在这里提供（通过每行的悬停动作）。
  if (view === "archived") {
    return (
      <>
        <button
          type="button"
          onClick={() => setView("history")}
          className="flex w-full items-center gap-1.5 rounded-md px-2 py-2 text-left text-caption font-medium text-muted-foreground outline-none transition-colors hover:bg-accent/50 hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
        >
          <ChevronLeft className="size-4 shrink-0" />
          <span className="truncate">{t(($) => $.list.archived_title)}</span>
          <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">
            {archivedSessions.length}
          </span>
        </button>
        {archivedSessions.map(renderRow)}
      </>
    );
  }

  // 历史（默认）视图：活跃行 + 一个进入归档的页脚入口。
  const archivedEntry = archivedSessions.length > 0 && (
    <button
      type="button"
      onClick={() => setView("archived")}
      className="mt-1 flex h-10 w-full items-center gap-2 rounded-md px-2 text-left text-caption text-muted-foreground outline-none transition-colors hover:bg-accent/50 hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
    >
      <span className="flex size-9 shrink-0 items-center justify-center">
        <Archive className="size-4" />
      </span>
      <span className="min-w-0 flex-1 truncate font-medium">{t(($) => $.list.archived_title)}</span>
      <span className="shrink-0 tabular-nums text-muted-foreground">{archivedSessions.length}</span>
      <ChevronRight className="size-4 shrink-0 text-faint-foreground" />
    </button>
  );

  if (historySessions.length === 0) {
    return (
      <>
        <div className="px-2 py-1.5 text-caption text-muted-foreground">
          {t(($) => $.window.no_previous)}
        </div>
        {archivedEntry}
      </>
    );
  }

  return (
    <>
      {historySessions.map(renderRow)}
      {archivedEntry}
    </>
  );
}

function RowAction({
  icon,
  label,
  onClick,
  danger,
}: {
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onPointerDown={(e) => {
        e.preventDefault();
        e.stopPropagation();
      }}
      onClick={(e) => {
        e.stopPropagation();
        e.preventDefault();
        onClick();
      }}
      className={cn(
        "inline-flex size-7 items-center justify-center rounded text-muted-foreground transition-colors focus-visible:outline-none",
        danger
          ? "hover:bg-destructive/10 hover:text-destructive focus-visible:bg-destructive/10 focus-visible:text-destructive"
          : "hover:bg-accent hover:text-foreground focus-visible:bg-accent focus-visible:text-foreground",
      )}
    >
      {icon}
    </button>
  );
}

function ConfirmRow({
  label,
  cancelText,
  confirmText,
  pending,
  onCancel,
  onConfirm,
}: {
  label: string;
  cancelText: string;
  confirmText: string;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2">
      <span className="min-w-0 flex-1 truncate text-caption font-medium text-destructive">{label}</span>
      <div className="flex shrink-0 items-center gap-1">
        <button
          type="button"
          onPointerDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
          }}
          onClick={(e) => {
            e.stopPropagation();
            e.preventDefault();
            onCancel();
          }}
          disabled={pending}
          className="inline-flex h-6 items-center rounded px-2 text-micro font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:opacity-50"
        >
          {cancelText}
        </button>
        <button
          type="button"
          onPointerDown={(e) => {
            e.preventDefault();
            e.stopPropagation();
          }}
          onClick={(e) => {
            e.stopPropagation();
            e.preventDefault();
            onConfirm();
          }}
          disabled={pending}
          className="inline-flex h-6 items-center rounded px-2 text-micro font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
        >
          {confirmText}
        </button>
      </div>
    </div>
  );
}
