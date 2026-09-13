// 本文件提供工作区「目录型」查询的 React Hook：工作区列表、成员/agent/squad
// 名单，以及把「类型 + id」解析成可展示的名字/头像的工具。

"use client";

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, MemberWithUser, Squad, Workspace } from "../types";
import { useWorkspaceId } from "../hooks";
import {
  memberListOptions,
  agentListOptions,
  squadListOptions,
  workspaceListOptions,
} from "./queries";
import { resolvePublicFileUrl } from "./avatar-url";
import { useFeatureEnabled } from "../config";
import { PLUGINS_V1_FLAG } from "../feature-flags";
import { pluginInstallationsOptions } from "../plugins";

// 仍处于加载中的目录查询要用「稳定的空数组」。若用 `= []` 这种每次渲染新建的
// 默认值，`data` 为 undefined 期间每一帧都会分配一个新数组，导致
// `useMemo(..., [members, agents, squads])` 在冷加载期每帧都重算
// `getActorName`。把 `getActorName` 放进自己 memo 依赖的消费方（BoardView 的
// `groups`、SwimLaneView 的 `laneGroups`）于是每帧都换新值，看板/列表列的
// resync `useEffect(setColumns, [groups])` 就会无限触发——react-virtuoso 把这种
// 死循环报成 Issues 路由上的 "Maximum update depth exceeded"（MUL-4985）。
// 共享同一引用，让加载中的快照在引用上保持稳定。
const EMPTY_MEMBERS: MemberWithUser[] = [];
const EMPTY_AGENTS: Agent[] = [];
const EMPTY_SQUADS: Squad[] = [];
const EMPTY_WORKSPACES: Workspace[] = [];

/** 名字解析需要拉取哪些目录的开关。 */
type ActorNameQueryScope = {
  members?: boolean;
  agents?: boolean;
  squads?: boolean;
  plugins?: boolean;
};

const DEFAULT_ACTOR_NAME_QUERY_SCOPE: Required<ActorNameQueryScope> = {
  members: true,
  agents: true,
  squads: true,
  plugins: true,
};

/**
 * 工作区列表的「权威状态已就绪」契约。
 *
 * TanStack Query 的 `isFetched` 在初次失败后也会变 true，所以它区分不了
 * 「账号没有任何工作区」和「第一个请求在列表到达前就失败了」。数据是否在场
 * 能区分：成功空响应是 `[]`，初次失败则保持 `undefined`。后台失败保留着缓存
 * 数据，因此仍视为就绪。
 *
 * @param options - 可选 enabled 开关
 */
export function useWorkspaceList({ enabled = true }: { enabled?: boolean } = {}) {
  const query = useQuery({
    ...workspaceListOptions(),
    enabled,
  });
  const ready = query.data !== undefined;

  return {
    workspaces: query.data ?? EMPTY_WORKSPACES,
    ready,
    unavailable: enabled && !ready && query.isLoadingError,
    isFetching: query.isFetching,
    refetch: query.refetch,
  };
}

/**
 * 基于显式目录快照的纯「操作者名字解析」。
 *
 * 异步流程（例如 CSV 导出）必须用自己 await 过的目录来解析名字——绑定在 hook
 * 上的 getActorName 闭包捕捉的是渲染时刻查询里有什么，目录还在加载时会静默地
 * 把所有人叫成 "Unknown*"。
 *
 * @param directories - 成员/agent/squad/插件目录快照
 * @returns (type, id) => 名字 的解析函数
 */
export function buildActorNameResolver(directories: {
  members: readonly { user_id: string; name: string }[];
  agents: readonly { id: string; name: string }[];
  squads: readonly { id: string; name: string }[];
  /**
   * 已安装的插件。可省，因为大多数调用方没有理由加载它们；事件写入的行于是
   * 回退成泛泛的 "Plugin"——模糊，但不像 "System" 那样是错的。
   */
  plugins?: readonly { id: string; name: string }[];
}) {
  const memberNames = new Map(directories.members.map((m) => [m.user_id, m.name]));
  const agentNames = new Map(directories.agents.map((a) => [a.id, a.name]));
  const squadNames = new Map(directories.squads.map((s) => [s.id, s.name]));
  const pluginNames = new Map((directories.plugins ?? []).map((p) => [p.id, p.name]));
  return (type: string, id: string) => {
    if (type === "member") return memberNames.get(id) ?? "Unknown";
    if (type === "agent") return agentNames.get(id) ?? "Unknown Agent";
    if (type === "squad") return squadNames.get(id) ?? "Unknown Squad";
    // 事件触发的 hook 以「安装本身」的身份写入：背后没有具体的人，借用最后
    // 碰过这个 issue 的成员做署名是审计日志无法撤销的谎言。id 解析不到意味着
    // 插件已被卸载——行仍应保持可读。
    if (type === "plugin") return pluginNames.get(id) ?? "Plugin";
    if (type === "system") return "Multica";
    return "System";
  };
}

/**
 * 当前工作区的操作者名解析 hook：按需拉取成员/agent/squad（及插件）目录，
 * 返回稳定的取名/取头像方法集合。
 *
 * @param scope - 要拉取的目录开关
 */
export function useActorName(
  scope: ActorNameQueryScope = DEFAULT_ACTOR_NAME_QUERY_SCOPE,
) {
  const wsId = useWorkspaceId();
  const { data: members = EMPTY_MEMBERS } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId && scope.members !== false,
  });
  const { data: agents = EMPTY_AGENTS } = useQuery({
    ...agentListOptions(wsId),
    enabled: !!wsId && scope.agents !== false,
  });
  const { data: squads = EMPTY_SQUADS } = useQuery({
    ...squadListOptions(wsId),
    enabled: !!wsId && scope.squads !== false,
  });
  // 只为给插件署名的行取名。受特性开关约束，避免没有插件的工作区拉一份它
  // 永远渲染不出作者的列表。
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);
  const { data: pluginData } = useQuery({
    ...pluginInstallationsOptions(wsId),
    enabled: pluginsEnabled && wsId.length > 0 && scope.plugins !== false,
  });

  const getMemberName = useCallback((userId: string) => {
    const m = members.find((m) => m.user_id === userId);
    return m?.name ?? "Unknown";
  }, [members]);

  const getAgentName = useCallback((agentId: string) => {
    const a = agents.find((a) => a.id === agentId);
    return a?.name ?? "Unknown Agent";
  }, [agents]);

  const getSquadName = useCallback((squadId: string) => {
    const s = squads.find((s) => s.id === squadId);
    return s?.name ?? "Unknown Squad";
  }, [squads]);

  const getActorName = useMemo(
    () => buildActorNameResolver({ members, agents, squads, plugins: pluginData?.plugins }),
    [agents, members, squads, pluginData],
  );

  const getActorInitials = useCallback((type: string, id: string) => {
    const name = getActorName(type, id);
    return name
      .split(" ")
      .map((w) => w[0])
      .join("")
      .toUpperCase()
      .slice(0, 2);
  }, [getActorName]);

  const getActorAvatarUrl = useCallback((type: string, id: string): string | null => {
    if (type === "member") return resolvePublicFileUrl(members.find((m) => m.user_id === id)?.avatar_url);
    if (type === "agent") return resolvePublicFileUrl(agents.find((a) => a.id === id)?.avatar_url);
    if (type === "squad") return resolvePublicFileUrl(squads.find((s) => s.id === id)?.avatar_url);
    return null;
  }, [agents, members, squads]);

  return useMemo(
    () => ({
      getMemberName,
      getAgentName,
      getSquadName,
      getActorName,
      getActorInitials,
      getActorAvatarUrl,
    }),
    [
      getActorAvatarUrl,
      getActorInitials,
      getActorName,
      getAgentName,
      getMemberName,
      getSquadName,
    ],
  );
}
