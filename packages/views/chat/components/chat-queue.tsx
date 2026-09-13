// 聊天排队任务卡片：agent 忙时被排队的后续请求列在这里（缩在输入框下沿），
// 支持"立即发送"（抢占/引导）、编辑、删除与一键清空。
"use client";

import { useState } from "react";
import {
  CornerDownRight,
  Ellipsis,
  ListEnd,
  Loader2,
  Pencil,
  Trash2,
} from "lucide-react";
import type { ChatQueuedTask } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";

interface ChatQueueProps {
  tasks: ChatQueuedTask[];
  headStatus: string | undefined;
  onSendNow: (taskId: string) => Promise<void> | void;
  /** 独立于队头任务状态地禁用"立即发送"——当调用方已经无权调用该 agent 时使用，
   *  因为引导一个排队任务会派发一次服务端会拒绝的运行（MUL-6380）。 */
  sendNowDisabled?: boolean;
  onEdit: (taskId: string) => Promise<void> | void;
  onRemove: (taskId: string) => Promise<void> | void;
  onClear: () => Promise<void> | void;
}

/**
 * 输入框下方的排队任务卡片。任务排在 agent 后面依次执行；用户可把某个任务
 * 提前为队头"立即发送"（dispatch），或编辑 / 删除单个任务、清空整条队列。
 * 无任务时不渲染。
 */
export function ChatQueue({
  tasks,
  headStatus,
  onSendNow,
  sendNowDisabled = false,
  onEdit,
  onRemove,
  onClear,
}: ChatQueueProps) {
  const { t } = useT("chat");
  const [busyAction, setBusyAction] = useState<string | null>(null);
  const dispatchableHead =
    headStatus === "dispatched" ||
    headStatus === "running" ||
    headStatus === "waiting_local_directory";
  const canSendNow = !sendNowDisabled && dispatchableHead;
  // 两种被阻塞的状态文案不同："等回复开始"是可行动的，"你不能运行这个 agent"
  // 不是——让用户去等一件等待也解决不了的事，本身就是 bug（MUL-6380）。
  const sendNowLabel = t(($) =>
    canSendNow
      ? $.queue.steer
      : sendNowDisabled
        ? $.queue.steer_no_permission
        : $.queue.steer_unavailable,
  );

  if (tasks.length === 0) return null;

  const run = async (key: string, action: () => Promise<void> | void) => {
    setBusyAction(key);
    try {
      await action();
    } finally {
      setBusyAction((current) => current === key ? null : current);
    }
  };

  return (
    <div
      data-slot="chat-queue-shell"
      // 缩在输入框下沿：负外边距把卡片的底边滑到输入框（不透明、恒为 z-10）表面的
      // 后面，让队列看起来像从输入框背后冒出来。z-0 保证即使入场动画的透明度/位移
      // 创建了层叠上下文，它也不会盖过输入框。输入框自身的外观永远不变。
      className={cn(CHAT_GUTTER, "relative z-0 -mb-3")}
      aria-live="polite"
      aria-busy={busyAction !== null}
    >
      <div className={CHAT_COLUMN}>
        <section
          data-slot="chat-queue"
          aria-label={t(($) => $.queue.title, { count: tasks.length })}
          className={cn(
            // 安静的次级表面：只有边框、没有阴影，让下方的输入框在视觉上仍是主卡片。
            // pb-4 = 藏在输入框后面的 12px 加上上方可见的 4px 呼吸空间。
            // mx-3 把这张后层卡片从输入框边缘向内收，让堆叠读起来像两层。
            "mx-3 overflow-hidden rounded-lg border border-surface-border bg-surface pb-4",
            "animate-in fade-in slide-in-from-bottom-2 duration-300",
          )}
        >
          <div
            data-slot="chat-queue-list"
            className="max-h-40 overflow-y-auto px-2 py-1"
          >
            {tasks.map((task) => {
              const sendNowKey = `send-now:${task.task_id}`;
              const editKey = `edit:${task.task_id}`;
              const removeKey = `remove:${task.task_id}`;
              const clearKey = `clear:${task.task_id}`;
              return (
                <div
                  key={task.task_id}
                  data-slot="chat-queue-row"
                  className="flex min-h-7 min-w-0 items-center gap-2 rounded-md px-1.5 py-0.5 text-caption animate-in fade-in duration-200"
                >
                  <ListEnd
                    data-slot="chat-queue-item-icon"
                    className="size-3.5 shrink-0 text-faint-foreground"
                    aria-hidden="true"
                  />
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {task.content?.trim() || t(($) => $.queue.fallback)}
                  </span>
                  <div className="flex shrink-0 items-center gap-0.5">
                    <span
                      className="shrink-0"
                      title={sendNowLabel}
                    >
                      <Button
                        variant="ghost"
                        size="xs"
                        className="px-1.5 font-normal text-muted-foreground"
                        disabled={busyAction !== null || !canSendNow}
                        aria-label={sendNowLabel}
                        onClick={() => void run(sendNowKey, () => onSendNow(task.task_id))}
                      >
                        {busyAction === sendNowKey ? (
                          <Loader2 className="animate-spin" aria-hidden="true" />
                        ) : (
                          <CornerDownRight aria-hidden="true" />
                        )}
                        {t(($) => $.queue.steer)}
                      </Button>
                    </span>
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      className="shrink-0 text-muted-foreground"
                      disabled={busyAction !== null}
                      title={t(($) => $.queue.remove)}
                      aria-label={t(($) => $.queue.remove)}
                      onClick={() => void run(removeKey, () => onRemove(task.task_id))}
                    >
                      {busyAction === removeKey ? (
                        <Loader2 className="animate-spin" aria-hidden="true" />
                      ) : (
                        <Trash2 aria-hidden="true" />
                      )}
                    </Button>
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-xs"
                            className="shrink-0 text-muted-foreground"
                            disabled={busyAction !== null}
                            title={t(($) => $.queue.more)}
                            aria-label={t(($) => $.queue.more)}
                          />
                        }
                      >
                        {busyAction === editKey || busyAction === clearKey ? (
                          <Loader2 className="animate-spin" aria-hidden="true" />
                        ) : (
                          <Ellipsis aria-hidden="true" />
                        )}
                      </DropdownMenuTrigger>
                      <DropdownMenuContent
                        align="end"
                        side="top"
                        sideOffset={6}
                        className="w-auto"
                      >
                        <DropdownMenuItem
                          disabled={busyAction !== null}
                          onClick={() => void run(editKey, () => onEdit(task.task_id))}
                        >
                          <Pencil aria-hidden="true" />
                          {t(($) => $.queue.edit)}
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant="destructive"
                          disabled={busyAction !== null}
                          onClick={() => void run(clearKey, onClear)}
                        >
                          <Trash2 aria-hidden="true" />
                          {t(($) => $.queue.clear)}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </div>
              );
            })}
          </div>
        </section>
      </div>
    </div>
  );
}
