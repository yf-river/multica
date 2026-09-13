"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Plug } from "lucide-react";
import { toast } from "sonner";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { useCurrentWorkspace } from "@multica/core/paths";
import { pluginInstallationsOptions, useInvokePluginHook } from "@multica/core/plugins";
import type { PluginHook, PluginInstallation } from "@multica/core/types";
import { useT } from "../i18n";

/**
 * plugins 模块：issue 上的人工 hook 操作（菜单项与执行）。
 *
 * `manual` 是「有人从菜单里挑了这个」的触发器，区别于 `ui`（插件在自己面板
 * 里画的一个按钮）和 `event`（没有人主动挑选）。这个区别不是装饰性的——它
 * 决定了写入结果携带的是谁的身份——所以这个列表由宿主持有，而不是让 surface
 * 渲染自己的菜单项。
 *
 * 与 Quick Actions 列表一样，这里不按「调用是否可能成功」过滤。隐藏一个当前
 * 端点挂了的行为，会让同一 issue 上的两个人看到不同的菜单且没有任何解释；
 * 拒绝发生时本身会自我解释。
 */

/** 一条可执行的插件 hook 操作：安装实例 + 其中的某个 hook。 */
export interface PluginHookAction {
  installation: PluginInstallation;
  hook: PluginHook;
}

/** 生成一条 hook 操作在菜单/状态里用的稳定 key。 */
export function pluginHookActionKey(action: PluginHookAction): string {
  return `${action.installation.id}:${action.hook.key}`;
}

/**
 * 收集所有已启用安装里声明了 `manual` 触发器的 hook。
 *
 * 单独导出是为了它自己的测试：这个过滤是产品规则（启用 + 声明 manual），
 * 应该只放在一个地方，而不是在每个菜单里各推导一遍。
 */
export function collectManualHookActions(installations: readonly PluginInstallation[]): PluginHookAction[] {
  const actions: PluginHookAction[] = [];
  for (const installation of installations) {
    // 按 API 兼容规则显式用 === true：后端一旦不再发送该字段，必须读作
    //「关闭」而不是「足够真」。
    if (installation.enabled !== true) continue;
    for (const hook of installation.hooks ?? []) {
      if ((hook.triggers ?? []).includes("manual")) actions.push({ installation, hook });
    }
  }
  return actions;
}

/** 返回当前工作区所有启用的 manual hook 操作（未启用插件时为空数组）。 */
export function usePluginHookActions(): PluginHookAction[] {
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);
  const wsId = useCurrentWorkspace()?.id ?? "";
  const { data } = useQuery({
    ...pluginInstallationsOptions(wsId),
    enabled: pluginsEnabled && wsId.length > 0,
  });

  return useMemo(
    () => (pluginsEnabled ? collectManualHookActions(data?.plugins ?? []) : []),
    [data, pluginsEnabled],
  );
}

/**
 * 执行一个 manual hook 并汇报结果。
 *
 * 提示语刻意按结果分型。「refused」意味着调用根本没有离开我们——管理员未
 * 授予该权限、触发限流、安装被禁用——这时告诉用户「你的插件坏了」会让他们
 * 去调试错误的系统，而真相是「没人批准这个」。
 */
export function useRunPluginHook() {
  const { t } = useT("issues");
  const invoke = useInvokePluginHook();
  const [running, setRunning] = useState<string | null>(null);

  const run = async (action: PluginHookAction, issueId?: string) => {
    setRunning(pluginHookActionKey(action));
    try {
      const result = await invoke.mutateAsync({
        installationId: action.installation.id,
        hookKey: action.hook.key,
        trigger: "manual",
        issueId,
      });
      if (result.status === "ok") {
        toast.success(t(($) => $.plugins.hook_succeeded, { name: action.hook.name }));
      } else if (result.status === "refused") {
        toast.error(t(($) => $.plugins.hook_refused, { name: action.hook.name }), {
          description: result.error,
        });
      } else {
        toast.error(t(($) => $.plugins.hook_failed, { name: action.hook.name }), {
          description: result.error,
        });
      }
      return result;
    } catch (error) {
      toast.error(t(($) => $.plugins.hook_failed, { name: action.hook.name }), {
        description: error instanceof Error ? error.message : undefined,
      });
      return null;
    } finally {
      setRunning(null);
    }
  };

  return { run, running };
}

/** 插件 hook 菜单项的 props。 */
interface PluginHookMenuItemsProps {
  issueId: string;
  /**
   * 宿主菜单自己的 Item 组件。以 prop 而非 import 的方式传入，让同一份列表
   * 既能进下拉菜单也能进右键菜单——这也是 `IssueActionsMenuItems` 已经在用的
   * 模式，正是两者不会分叉的原因。
   */
  Item: React.ComponentType<{
    onClick?: () => void;
    disabled?: boolean;
    children?: React.ReactNode;
  }>;
  Separator?: React.ComponentType<Record<string, never>>;
}

/**
 * 渲染一组「运行插件 hook」的菜单项。没有可用 hook 时返回 null。
 * 菜单项同时展示 hook 名与插件名，执行中该项转圈并禁用。
 */
export function PluginHookMenuItems({ issueId, Item, Separator }: PluginHookMenuItemsProps) {
  const actions = usePluginHookActions();
  const { run, running } = useRunPluginHook();

  if (actions.length === 0) return null;

  return (
    <>
      {Separator ? <Separator /> : null}
      {actions.map((action) => {
        const key = pluginHookActionKey(action);
        const isRunning = running === key;
        return (
          <Item key={key} disabled={isRunning} onClick={() => void run(action, issueId)}>
            {isRunning ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Plug className="h-3.5 w-3.5" />}
            {/* 展示的是插件名，而不只是动作名：两个已装插件可能都贡献了
                「Summarize」，菜单里重复出现同一个词，用户就没法选了。 */}
            <span className="truncate">{action.hook.name}</span>
            <span className="ml-auto truncate pl-3 text-caption text-muted-foreground">
              {action.installation.name}
            </span>
          </Item>
        );
      })}
    </>
  );
}
