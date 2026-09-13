"use client";

import {
  createContext,
  use,
  useCallback,
  useEffect,
  useMemo,
  useState,
  useTransition,
} from "react";
import type { NavigationAdapter } from "./types";

const NavigationContext = createContext<NavigationAdapter | null>(null);
const NavigationPendingContext = createContext<boolean>(false);

/** 一次被上报的切换在途时 `+1`，落定后 `-1`。 */
type PendingReporter = (delta: 1 | -1) => void;
const NavigationPendingReportContext = createContext<PendingReporter | null>(
  null,
);

/**
 * 向子树提供导航适配器。把 push/replace 包进 startTransition，并用第二路
 * 「上报式 pending」覆盖页面内内容切换，汇总为统一的导航中信号。
 */
export function NavigationProvider({
  value,
  children,
}: {
  value: NavigationAdapter;
  children: React.ReactNode;
}) {
  // 把 push/replace 包进 startTransition，使任何 useNavigation() 的调用方
  // （侧边栏 AppLink、命令面板、建完 post-create 跳转）在路由提交期间都能
  // 拿到 React 的 pending 信号。Web 上它会一直为 true 直到 Next.js 提交新的
  // RSC payload；桌面端因 react-router 同步提交而快速熄灭——两者都正确。
  const [isPending, startTransition] = useTransition();
  // 第二路 pending 来源：通过 `useReportNavigating` 上报的页内内容切换。
  // 上面的 transition 只覆盖适配器自己的 push/replace，而桌面端那是同步的
  // store 写入——目标内容还在渲染它就已落定，单靠它永远没法让进度条留在
  // 屏幕上了。
  const [reportedPending, setReportedPending] = useState(0);
  const report = useCallback<PendingReporter>((delta) => {
    setReportedPending((n) => Math.max(0, n + delta));
  }, []);
  const wrapped = useMemo<NavigationAdapter>(
    () => ({
      ...value,
      push: (path: string) => startTransition(() => value.push(path)),
      replace: (path: string) => startTransition(() => value.replace(path)),
    }),
    [value],
  );
  return (
    <NavigationContext.Provider value={wrapped}>
      <NavigationPendingReportContext.Provider value={report}>
        <NavigationPendingContext.Provider
          value={isPending || reportedPending > 0}
        >
          {children}
        </NavigationPendingContext.Provider>
      </NavigationPendingReportContext.Provider>
    </NavigationContext.Provider>
  );
}

/**
 * 不抛错的适配器读取。供那些合法地渲染在 provider 之外、而依赖导航的行为
 * 又有合理兜底的组件使用——独立挂载的叶子编辑器 UI，以及测试它们的用例。
 * 任何必须导航的地方都应该用 `useNavigation()`，并保留它的抛错行为。
 */
export function useOptionalNavigation(): NavigationAdapter | null {
  return use(NavigationContext);
}

/**
 * 读取当前导航适配器。必须在 NavigationProvider 内使用，否则抛错。
 */
export function useNavigation(): NavigationAdapter {
  const ctx = use(NavigationContext);
  if (!ctx)
    throw new Error("useNavigation must be used within NavigationProvider");
  return ctx;
}

/**
 * 当一次 transition 包裹的 push/replace 正在提交、或有任意挂载中的视图上报
 * 页内内容切换时为 true。
 */
export function useIsNavigating(): boolean {
  return use(NavigationPendingContext);
}

/**
 * 把一次页内内容切换上报给外壳的 `<NavigationProgress />`。
 *
 * 针对那些永远不会改变路由的目的地——收件箱的详情面板正是为此存在的场景。
 * 那里点击立即更新列表高亮与 URL，而昂贵的那部分（挂载新的 issue 详情）在
 * `useDeferredValue` 后面跑，所以用户等待时，导航适配器能看到的任何东西都
 * 已不处于 pending。把这个缺口上报进来，让真实路由切换获得的同一条进度条
 * 也能覆盖它。
 *
 * 在 `NavigationProvider` 之外调用是安全的（独立挂载的叶子视图，以及它们的
 * 测试）——它只是无从上报而已。
 */
export function useReportNavigating(pending: boolean): void {
  const report = use(NavigationPendingReportContext);
  useEffect(() => {
    if (!pending || !report) return;
    report(1);
    return () => report(-1);
  }, [pending, report]);
}
