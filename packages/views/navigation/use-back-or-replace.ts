"use client";

import { useCallback } from "react";
import { useNavigation } from "./context";

/**
 * navigation 模块：离开「主体已不存在」的页面，把用户送回他来的地方。
 *
 * 从详情页删除一个 issue 就是典型场景：用户可能是从 My Issues、项目列表、
 * 搜索结果或置顶项进来的，而每一条都比写死的工作区列表更好的目的地。「后退」
 * 是唯一知道是哪个的来源——调用方不必把「来源视图」穿过 URL 或 store 传下来。
 *
 * `fallback` 覆盖「无可后退」的情况：冷打开的分享链接、新标签页、粘贴的
 * URL。从这些地方后退会整个离开 Multica，所以改为导航到 `fallback`。
 *
 * 它同样覆盖所有回答不了该问题的平台——桌面端 issue 窗口、测试桩、以及没有
 * Navigation API 的浏览器。那个分支正是这些界面在本 hook 出现前的行为，所以
 * 平台一旦沉默，用户损失只是一个更好的目的地，仅此而已。`canGoBack` 只能由
 * 真正知道答案的东西来回答；这里抱乐观乱猜，就会把用户带出应用。
 *
 * 永远不 `push`：我们离开的页面已经死了，它的 URL 不能留在返回栈上让返回键
 * 落进一个 404。后退仍会把它留在我们前方的历史里——那是浏览器自己的契约，
 * 只有显式 Forward 才够得着。
 */
export function useBackOrReplace(): (fallback: string) => void {
  const { back, replace, canGoBack } = useNavigation();
  return useCallback(
    (fallback: string) => {
      if (canGoBack?.() === true) back();
      else replace(fallback);
    },
    [back, replace, canGoBack],
  );
}
