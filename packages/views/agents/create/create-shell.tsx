"use client";

// 本文件提供创建 Agent 各路由共用的“页面外壳”：顶部返回按钮 + 流程标题 + 当前
// 步骤 + 右侧上下文徽章。每条路由自己定义“返回”的含义——方式选择页是离开流程，
// 两个方法页是回到选择页——所以外壳接收 handler 而非自行推导。

import type { ReactNode } from "react";
import { ArrowLeft } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

/**
 * 创建流程路由共用的外壳：返回控件、流程标题、当前步骤。
 * 每条路由自己决定“返回”行为——方式选择页离开流程、两个方法页回到选择页——
 * 所以这里收一个 handler 而不是自行推导。
 * @param chips 右侧对齐的上下文徽章（方式、运行环境），小屏隐藏。
 */
export function AgentCreateShell({
  title,
  step,
  onBack,
  backDisabled = false,
  chips,
  children,
}: {
  title: string;
  step: string;
  onBack: () => void;
  backDisabled?: boolean;
  /** 右对齐的上下文徽章（方式、运行环境）。小屏隐藏。 */
  chips?: ReactNode;
  children: ReactNode;
}) {
  const { t } = useT("agents");
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <header className="flex h-14 shrink-0 items-center gap-3 border-b px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onBack}
          disabled={backDisabled}
          aria-label={t(($) => $.creation_studio.back)}
        >
          <ArrowLeft className="size-4" aria-hidden="true" />
        </Button>
        <div className="min-w-0">
          <h1 className="truncate text-body font-semibold">{title}</h1>
          <p className="truncate text-caption text-muted-foreground">{step}</p>
        </div>
        {chips ? (
          <div className="ml-auto hidden items-center gap-2 text-caption text-muted-foreground sm:flex">
            {chips}
          </div>
        ) : null}
      </header>
      {children}
    </div>
  );
}

/** 一个胶囊形态的上下文徽章。 */
export function AgentCreateChip({ children }: { children: ReactNode }) {
  return <span className="rounded-full bg-muted px-2 py-1">{children}</span>;
}
