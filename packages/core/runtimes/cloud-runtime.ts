// 云 Runtime（基础设施节点）的查询与变更：节点列表（含启动中的轮询刷新）、创建、删除。
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";

/** 一个云 Runtime 节点的形状（EC2 等基础设施实例的映射）。 */
export interface CloudRuntimeNode {
  id: string;
  owner_id: string;
  instance_id: string;
  region: string;
  instance_type: string;
  image_id: string;
  subnet_id: string;
  name: string;
  status: string;
  tags: Record<string, string>;
  metadata: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

/** 列表接口的分页参数。 */
export interface ListCloudRuntimeNodesParams {
  limit?: number;
  offset?: number;
}

/** 创建节点请求的参数（基础设施相关字段均可选，交给默认值）。 */
export interface CreateCloudRuntimeNodeRequest {
  instance_type: string;
  name?: string;
  region?: string;
  image_id?: string;
  subnet_id?: string;
  key_name?: string;
  iam_instance_profile?: string;
  disk_size_gb?: number;
  tags?: Record<string, string>;
}

/** 云 Runtime 的缓存 key 工厂。 */
export const cloudRuntimeKeys = {
  all: (wsId: string) => ["cloud-runtime", wsId] as const,
  nodes: (wsId: string) => [...cloudRuntimeKeys.all(wsId), "nodes"] as const,
};

/** 节点正处于"生命周期迁移中"的状态集合（需要轮询等终态）。 */
const PENDING_NODE_STATUSES = new Set([
  "launching",
  "pending",
  "starting",
  "stopping",
  "rebooting",
  "terminating",
]);

/** 该节点状态是否处于"启动/迁移中"的过渡态。 */
export function isCloudRuntimeNodePending(status: string): boolean {
  return PENDING_NODE_STATUSES.has(status.toLowerCase());
}

/** 云 Runtime 节点列表查询配置：有节点处于过渡态时每 5s 自动轮询。 */
export function cloudRuntimeNodeListOptions(
  wsId: string,
  params?: ListCloudRuntimeNodesParams,
) {
  const limit = params?.limit ?? 20;
  const offset = params?.offset ?? 0;
  return queryOptions({
    queryKey: [...cloudRuntimeKeys.nodes(wsId), { limit, offset }] as const,
    queryFn: () => api.listCloudRuntimeNodes({ limit, offset }),
    // 任一节点还在启动/迁移中就持续轮询，直到全部安定。
    refetchInterval: (query) =>
      query.state.data?.some((node) => isCloudRuntimeNodePending(node.status))
        ? 5000
        : false,
    staleTime: 15 * 1000,
  });
}

/** 创建云 Runtime 节点的 mutation hook（成功后失效该工作区节点缓存）。 */
export function useCreateCloudRuntimeNode(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateCloudRuntimeNodeRequest) =>
      api.createCloudRuntimeNode(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: cloudRuntimeKeys.all(wsId) });
    },
  });
}

/** 删除云 Runtime 节点的 mutation hook（成功后失效该工作区节点缓存）。 */
export function useDeleteCloudRuntimeNode(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (instanceId: string) => api.deleteCloudRuntimeNode(instanceId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: cloudRuntimeKeys.all(wsId) });
    },
  });
}
