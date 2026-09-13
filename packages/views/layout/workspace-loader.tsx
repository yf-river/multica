"use client";

import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { useT } from "../i18n";

/**
 * 布局模块：全屏的工作区加载占位组件。
 * 在以下两种情况下替代整个仪表盘渲染：
 *  - 仪表盘首次挂载（正在根据 URL slug 和工作区列表缓存解析当前工作区）
 *  - 切换工作区（带着新的请求头重新拉取核心工作区数据）
 *
 * 这是一个「闸门」而不是浮层——侧边栏和内容区不会在它背后渲染。
 * 只有当当前工作区 id 写入工作区存储单例、并且目标工作区的所有核心
 * 查询都重新拉取完成后，闸门才会打开。
 * @param name 正在加载的工作区名称，用于在加载文案里显示。
 */
export function WorkspaceLoader({ name }: { name?: string | null }) {
  const { t } = useT("layout");
  return (
    <div
      className="flex h-svh w-full items-center justify-center bg-background"
      aria-live="polite"
      role="status"
    >
      <div className="flex flex-col items-center gap-4">
        <MulticaIcon className="size-8 animate-pulse" />
        {name ? (
          <p className="text-body text-muted-foreground">
            {t(($) => $.workspace_loader.loading_named_prefix)}{" "}
            <span className="font-medium text-foreground">{name}</span>…
          </p>
        ) : (
          <p className="text-body text-muted-foreground">{t(($) => $.workspace_loader.loading_workspace)}</p>
        )}
      </div>
    </div>
  );
}
