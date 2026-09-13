// 本文件负责创建并配置全局的 React Query 查询客户端（缓存策略、重试等）。

import { QueryClient } from "@tanstack/react-query";

/**
 * 创建并配置一个 React Query 客户端实例。
 *
 * 配置要点：
 * - `staleTime: Infinity`：数据一旦缓存永不过期，避免重复请求；
 * - `gcTime: 10 分钟`：闲置 10 分钟后才回收缓存；
 * - 失焦不重新请求，断线重连才重新请求；
 * - 查询失败重试 1 次，变更（mutation）不重试。
 *
 * @returns 配置好的 React Query 客户端
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: Infinity,
        gcTime: 10 * 60 * 1000, // 10 分钟
        refetchOnWindowFocus: false,
        refetchOnReconnect: true,
        retry: 1,
      },
      mutations: {
        retry: false,
      },
    },
  });
}
