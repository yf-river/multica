"use client";

// 工作区级"呈现状态预热"hook：提前订阅承载 Agent 呈现与 @提及 建议列表的查询，
// 让首次出现悬停卡/内联指示器/提及弹窗时数据已是热的。
import { useQuery } from "@tanstack/react-query";
import { agentListOptions, squadListOptions } from "../workspace/queries";
import { runtimeListOptions } from "../runtimes/queries";
import { agentTaskSnapshotOptions } from "./queries";

// 订阅承载 Agent 呈现状态与 @提及 建议列表的查询，让任何悬停卡 / 内联指示器 /
// 提及弹窗首次渲染时它们已是热的。没有这次预热，其它不碰快照的界面（inbox、
// issues、chat）首次悬停时会闪骨架屏（拉取进行中），@提及 列表也可能显示不完整
// （例如缺 squads）。
//
// useRealtimeSync（WS 的任务 / Agent / daemon / squad 失效）和 30s 呈现心跳
// 在首拉之后保持这些缓存新鲜——本 hook 只负责压缩冷启动窗口。
//
// 所有查询都是工作区作用域；queryKey 含 wsId，所以切工作区会自动重取新工作区的
// 数据，无需这里的额外接线。两个应用的工作区布局都以"工作区已解析"来门控渲染，
// 所以调用方可以放心传 useWorkspaceId()——本 hook 挂载时 wsId 已保证非空。
export function useWorkspacePresencePrefetch(wsId: string | undefined): void {
  useQuery({ ...agentListOptions(wsId ?? ""), enabled: !!wsId });
  useQuery({ ...runtimeListOptions(wsId ?? ""), enabled: !!wsId });
  useQuery({ ...agentTaskSnapshotOptions(wsId ?? ""), enabled: !!wsId });
  useQuery({ ...squadListOptions(wsId ?? ""), enabled: !!wsId });
}
