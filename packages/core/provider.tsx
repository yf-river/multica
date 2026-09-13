// 本文件提供 React Query 的 Provider 组件，负责把全局查询客户端注入到组件树中。

"use client";

import { useState } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createQueryClient } from "./query-client";
import type { ReactNode } from "react";

/**
 * React Query 的 Provider 组件，用一次创建并复用的查询客户端包裹子组件，
 * 让整棵组件树都能共享同一份服务端数据缓存。
 *
 * @param children - 需要被包裹、能够访问 React Query 能力的子组件
 */
export function QueryProvider({ children }: { children: ReactNode }) {
  // useState 的惰性初始化确保查询客户端在整个组件生命周期内只创建一次。
  const [queryClient] = useState(createQueryClient);
  return (
    <QueryClientProvider client={queryClient}>
      {children}
    </QueryClientProvider>
  );
}
