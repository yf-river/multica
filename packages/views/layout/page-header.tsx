"use client";

import { cn } from "@multica/ui/lib/utils";
import { SidebarTrigger, useSidebarSafe } from "@multica/ui/components/ui/sidebar";

/**
 * 布局模块：页面头部（页眉）与统一的左右留白常量。
 * 所有页面共用的左侧留白、页眉下的工具栏，以及收起状态下唤出导航的触发器。
 *
 * 之所以定义成常量而不是每个页面各自写 class，是因为一旦各自写就会漂移：
 * 集合页用过 `px-5`、issues 系列用过 `px-4`，切换标签时标题会平移 4px。
 * 所有需要对齐边缘的地方都导入这个常量，而不是重复写一遍 class；
 * 单独拼自己留白的页面，就是 bug 再次出现的地方。
 */
export const PAGE_GUTTER = "px-4";

/**
 * 紧贴在 `PageHeader` 下方的筛选/操作行：高度和留白一致，让二者读起来
 * 像同一个 chrome 区块。与 `PAGE_GUTTER` 共用的理由相同——当初各页面
 * 各自拼自己的工具栏时，它们之间出现了漂移。
 */
export const PAGE_TOOLBAR = cn(
  "flex h-12 shrink-0 items-center justify-between gap-2",
  PAGE_GUTTER,
);

/**
 * 在导航不是常驻列的地方提供「回到导航」的入口：紧凑断点以下是一个抽屉，
 * 从那里到 `xl` 之间则自动收起。
 *
 * `xl` 以下的每个界面都需要一个这样的触发器——除非外层外壳已经常驻了一个。
 * `PageHeader` 会免费提供它，所以这里导出它供那些自建 chrome 的页面使用；
 * 没有它，触屏用户就完全没有重新打开导航的入口。
 *
 * 在以下两种「它不是返回导航的入口」的情况下渲染为空：
 * 一是在 `SidebarProvider` 之外（独立页面，没有可重新打开的导航）；
 * 二是外层外壳声明了 `hasExternalTrigger` —— 桌面窗口工具栏的触发器
 * 永远不会滚走或隐藏，若再在每个页眉里放一份，就会在 50px 的间距里
 * 叠出两个一模一样的图标；详情面板自带页眉时还会出现第三个（MUL-6218）。
 */
export function CollapsedNavTrigger() {
  const sidebar = useSidebarSafe();
  if (!sidebar || sidebar.hasExternalTrigger) return null;
  return <SidebarTrigger className="xl:hidden" />;
}

/** 页眉组件的属性。 */
interface PageHeaderProps {
  children: React.ReactNode;
  /**
   * 替换最左侧的移动端侧边栏触发器。
   *
   * 对于手机通过「点进去」而非「导航过去」到达的界面——比如收件箱里的
   * issue 详情——「返回」才是最重要的引导入口，侧边栏还在它后面一步。
   * 两者都渲染会在页眉 48px 里花掉两份导航 chrome，让标题无处截断。
   */
  leading?: React.ReactNode;
  className?: string;
}

/**
 * 页眉组件：统一的顶部栏。
 * 用内容组的 `flex-1` 把操作推到右侧，而不是在页眉上用 `justify-between`。
 *
 * 下面的 leading 插槽也是 flex 子项，所以源码里读起来是两个区块的页眉，
 * 运行时其实是三个；`justify-between` 会在标题两侧同时分配空白，
 * 把标题停到页眉中间。桌面窗口位于 `xl` 以下（触发器会渲染的地方），
 * 所以问题会在这里暴露。
 *
 * 同样地，`gap-2` 作为基础间距，是让页眉自身的 gap 成为 leading 插槽
 * 间距的唯一来源。若触发器再自带 margin，每个声明了 gap 的页眉就会
 * 双重付费，标题会比没声明 gap 的更靠右。需要更多留白时按页眉覆盖 gap，
 * 而不要给触发器加 margin。
 *
 * 不要通过 `className` 传 `px-*`。留白对每个页面都是 `PAGE_GUTTER`，
 * 页眉下方的工具栏也读取同一个常量。
 * @param children 页眉内容（标题等）。
 * @param leading 最左侧的引导元素，默认是收起状态的导航触发器。
 * @param className 附加样式类名。
 */
export function PageHeader({ children, leading, className }: PageHeaderProps) {
  return (
    <header
      className={cn(
        "flex h-12 shrink-0 items-center gap-2 border-b",
        className,
        PAGE_GUTTER,
      )}
    >
      {leading ?? <CollapsedNavTrigger />}
      {children}
    </header>
  );
}
