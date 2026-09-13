"use client";

// 本文件是 AI 创建流程的底部提交栏：左边错误文案、右边“放弃草稿”与“创建并打开”。
// 两个出口放在一起——它们是同一问题的两个答案，并排阅读才能让破坏性那个一眼可辨。

import { Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * 创建流程的终点。两个出口都在这里——提交 Agent，或放弃这次尝试——因为它们是
 * 同一个问题的两个答案，并排看才让破坏性那个清晰可辨。若把“丢弃”控件放去别处
 *（比如 transcript 上的垃圾桶图标），读起来就像“清空这个聊天”而不是“扔掉我在
 * 建的东西”。
 *
 * `onDiscard` 可选：手动流程没有可放弃的草稿——用户一导航离开，它的表单就没了。
 */
export function CreateAgentFooter({
  canCreate,
  creating,
  squad,
  error,
  onCreate,
  onDiscard,
  discarding = false,
}: {
  canCreate: boolean;
  creating: boolean;
  squad: boolean;
  error: string | null;
  onCreate: () => void;
  onDiscard?: () => void;
  discarding?: boolean;
}) {
  const { t } = useT("agents");
  return (
    <div className="pe-chat-launcher sticky bottom-0 mt-8 flex items-center justify-between gap-3 border-t bg-background/95 py-3 pl-5 backdrop-blur">
      {error ? (
        <p
          role="alert"
          className="min-w-0 flex-1 break-words text-body text-destructive"
        >
          {error}
        </p>
      ) : null}
      {onDiscard ? (
        <Button
          type="button"
          variant="ghost"
          className="ml-auto shrink-0 text-muted-foreground hover:text-foreground"
          onClick={onDiscard}
          disabled={creating || discarding}
        >
          {t(($) => $.creation_studio.drafts.discard)}
        </Button>
      ) : null}
      <Button
        type="button"
        className={cn("shrink-0", !onDiscard && "ml-auto")}
        onClick={onCreate}
        disabled={!canCreate}
      >
        {creating && <Loader2 className="size-4 animate-spin" />}
        {creating
          ? t(($) => $.creation_studio.creating)
          : squad
            ? t(($) => $.creation_studio.create_and_add)
            : t(($) => $.creation_studio.create_and_open)}
      </Button>
    </div>
  );
}
