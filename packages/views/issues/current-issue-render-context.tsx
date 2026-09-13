"use client";

// 「当前正在渲染的内容属于哪个问题」的上下文：让共享的内容渲染器知道自己在为哪个问题渲染。
import {
  createContext,
  useContext,
  type ReactNode,
} from "react";

/** 当前渲染内容所属问题的身份信息。 */
export type CurrentIssueRenderContextValue = Readonly<{
  id: string;
  identifier: string;
}>;

/**
 * 当前渲染内容所属的问题身份。
 *
 * 故意默认成 null：内容渲染器被问题详情、收件箱、聊天等多个视图共用，
 * 只有明确的问题详情宿主才可以选择启用「当前问题」语义。
 */
const CurrentIssueRenderContext =
  createContext<CurrentIssueRenderContextValue | null>(null);

/**
 * 提供当前问题渲染上下文。
 * @param props.value - 当前问题的身份信息（可为 null，表示不属于某个具体问题）。
 * @param props.children - 需要消费该上下文的子内容。
 */
export function CurrentIssueRenderContextProvider({
  value,
  children,
}: {
  value: CurrentIssueRenderContextValue | null;
  children: ReactNode;
}) {
  return (
    <CurrentIssueRenderContext.Provider value={value}>
      {children}
    </CurrentIssueRenderContext.Provider>
  );
}

/**
 * 读取当前问题渲染上下文。
 * @returns 当前问题身份信息，无宿主时为 null。
 */
export function useCurrentIssueRenderContext(): CurrentIssueRenderContextValue | null {
  return useContext(CurrentIssueRenderContext);
}
