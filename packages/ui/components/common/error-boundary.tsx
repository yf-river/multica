"use client";
// 本文件是「区块级」错误边界组件：用它包住单个 UI 区块（时间线、评论列表、侧栏面板等），
// 让某个区块在渲染时抛错不会拖垮整个页面；默认提供一个适合区块内展示的小型报错面板。

import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "../ui/button";

/**
 * ErrorBoundary 组件的 props 定义。
 */
export interface ErrorBoundaryProps {
  /** 需要被错误边界保护的子内容 */
  children: ReactNode;
  /**
   * 捕获到错误时展示的自定义兜底 UI。回调会收到 `reset`，
   * 可用来渲染一个「重试」按钮。默认是一个适合区块内使用的小型内联面板，
   * 不会整页替换。
   */
  fallback?: (args: { error: Error; reset: () => void }) => ReactNode;
  /**
   * 错误上报钩子（埋点/日志）。捕获到错误时连同 React 错误信息
   * （组件栈）一起调用。
   */
  onError?: (error: Error, info: ErrorInfo) => void;
  /**
   * 当该数组里的任意值在两次渲染之间发生变化时，边界自动复位。
   * 用于在切换到新资源（例如不同的 issueId）时自动恢复，用户无需手动点「重试」。
   */
  resetKeys?: ReadonlyArray<unknown>;
}

/** ErrorBoundary 内部状态：只记录当前是否捕获到了错误 */
interface ErrorBoundaryState {
  error: Error | null;
}

/** 初始状态：没有错误 */
const INITIAL_STATE: ErrorBoundaryState = { error: null };

/**
 * 区块级错误边界。用它包住单个 UI 区块（时间线、评论列表、侧栏面板），
 * 让某个区块渲染时崩溃不会白屏整页。见 CLAUDE.md「API Response Compatibility」。
 *
 * 需要整页兜底时请优先用路由级错误 UI（Next.js error.tsx、路由错误元素）。
 * 本组件面向的是「页面内局部恢复」的场景。
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = INITIAL_STATE;

  /** 子组件渲染抛错时由 React 调用，返回的 state 会触发一次重新渲染 */
  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    this.props.onError?.(error, info);
    // 无论有没有传 onError 都打日志，避免漏配 onError 时错误堆栈被悄悄吞掉。
    // 这里直接用 console 即可——平台日志器目前还没绑定到 UI 层。
    console.error("ErrorBoundary caught:", error, info.componentStack);
  }

  /** 对比 resetKeys 是否有变化，有则在更新后自动复位（实现「切资源自动恢复」） */
  override componentDidUpdate(prevProps: ErrorBoundaryProps): void {
    if (this.state.error == null) return;
    const prev = prevProps.resetKeys;
    const next = this.props.resetKeys;
    // 两侧都没有 resetKeys 就不做自动恢复
    if (!prev || !next) return;
    if (prev.length !== next.length) {
      this.reset();
      return;
    }
    for (let i = 0; i < prev.length; i++) {
      if (!Object.is(prev[i], next[i])) {
        this.reset();
        return;
      }
    }
  }

  /** 复位到初始状态，清掉已捕获的错误，让子组件重新渲染 */
  reset = (): void => {
    this.setState(INITIAL_STATE);
  };

  override render(): ReactNode {
    const { error } = this.state;
    if (error == null) return this.props.children;
    if (this.props.fallback) {
      return this.props.fallback({ error, reset: this.reset });
    }
    return <DefaultFallback error={error} reset={this.reset} />;
  }
}

/** 默认兜底面板：显示报错文案和「Try again」重试按钮，适合区块内局部展示 */
function DefaultFallback({ error, reset }: { error: Error; reset: () => void }) {
  return (
    <div
      role="alert"
      className="flex flex-col items-start gap-3 rounded-md border border-dashed border-border bg-muted/30 p-4 text-body"
    >
      <div className="space-y-1">
        <p className="font-medium text-foreground">
          Something went wrong displaying this section.
        </p>
        <p className="text-muted-foreground">
          {error.message || "An unexpected error occurred."}
        </p>
      </div>
      <Button size="sm" variant="outline" onClick={reset}>
        Try again
      </Button>
    </div>
  );
}
