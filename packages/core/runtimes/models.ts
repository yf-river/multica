// Runtime 模型目录（models）的发现与轮询查询：
// 发起 list-models 请求（经心跳捎带）后轮询，直到 daemon 回报或超时。
import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { RuntimeModelsResult } from "../types/agent";

/** 模型查询的缓存 key 工厂。 */
export const runtimeModelsKeys = {
  all: () => ["runtimes", "models"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeModelsKeys.all(), runtimeId] as const,
};

const POLL_INTERVAL_MS = 500;

// 客户端绝不能对服务端仍视为"活着"的请求提前放弃。两个窗口是顺序的、不重叠
// （server/internal/handler/runtime_models.go）：
//
//   - pending：从 CreatedAt 起算，daemon 最多有 30s 经心跳 CLAIM 该请求。
//   - running：从 RunStartedAt 起算，PopPending 在认领时设置它。心跳拾取发生在认领
//     之前、且受 pending 窗口约束，所以不会吃掉这段 60s。
//
// 一条在第 29s 被认领、随后跑 hermes 约 25-40s 发现的请求会在约 69s 回报——已经超过
// 60s 的客户端预算，而服务端仍持有着记录。用户会看到一条笼统的客户端"超时"，而不是
// daemon 正要送达的原因——对 hermes 来说那会指明要跑的确切命令（MUL-6606）。
// 预算按总和算，就消除了整类过早放弃，也让服务端自己的超时文案
// （"daemon 未在 30 秒内响应"）胜过我们的——它是两者里更具体的那条。
//
// 在这里多等对用户没有损失：选择器的手输输入在整段轮询期间保持可用，所以无需等待
// 发现就能键入并保存一个模型 id。
const SERVER_PENDING_TIMEOUT_MS = 30_000;
const SERVER_RUNNING_TIMEOUT_MS = 60_000;
// 为轮询间隔、store 自身清扫粒度与最后一次 GET 的网络延迟留余量——没有它，客户端
// 仍可能在服务端转换记录的同拍内过期。
const POLL_SLACK_MS = 10_000;
const POLL_TIMEOUT_MS =
  SERVER_PENDING_TIMEOUT_MS + SERVER_RUNNING_TIMEOUT_MS + POLL_SLACK_MS;

// 一份"活的"发现结果（daemon 刚产出）被信任多久、不必重新询问。发现是一次到用户
// 机器的往返；目录只在他们升级 CLI、换账号或改 provider 配置时变化，所以在同一表单
// 会话里重跑纯属浪费。
export const LIVE_MODELS_STALE_TIME_MS = 5 * 60_000;
// 保持较长，让同一会话稍后重访的 runtime 从客户端缓存渲染，而不是转"正在发现模型"
// 的 spinner。新鲜度由 staleTime 决定；gcTime 只决定条目闲置多久后从缓存清掉。
export const MODELS_GC_TIME_MS = 30 * 60_000;

/**
 * 发起模型发现请求并轮询到结果。
 *
 * resolveRuntimeModels 向 daemon 发起 list-models 请求（经心跳捎带），轮询直到 daemon
 * 回报或请求超时。返回模型列表 + `supported` 标记：`supported=false` 表示该 provider
 * 完全忽略按 Agent 的模型选择（qwenpaw 和 mcode——hermes 不在其中；它经 ACP
 * session/set_model 应用 opts.Model）——UI 用它禁用下拉，而不是接受一个运行时不会兑现
 * 的值。
 *
 * `cached` 报告服务器是从目录缓存作答、而非活 daemon 往返（MUL-5444）。它不只为
 * 好看：它喂给下面的 staleTime 策略，让客户端永不把服务端的陈旧窗口延长到服务端
 * 自己承诺之外。
 * @param runtimeId 目标 runtime id。
 * @returns 模型目录 + supported/cached 等元数据。
 * @throws 轮询超时或非 completed 终态时抛错。
 */
export async function resolveRuntimeModels(
  runtimeId: string,
): Promise<RuntimeModelsResult> {
  const initial = await api.initiateListModels(runtimeId);
  const start = Date.now();
  let current = initial;
  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - start > POLL_TIMEOUT_MS) {
      throw new Error("model discovery timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getListModelsResult(runtimeId, initial.id);
  }
  // 只有显式 `completed` 才是目录。任何其它——failed、timeout、或本客户端不认识
  // 的状态（更新服务器，或落到畸形记录形态的响应）——都以错误呈现，让选择器显示
  // "发现失败"并保留手输可用。把未识别状态当作成功会渲染一份看起来权威的空下拉。
  if (current.status !== "completed") {
    throw new Error(
      current.error || `model discovery failed (status: ${current.status})`,
    );
  }
  return {
    models: current.models ?? [],
    supported: current.supported !== false,
    cached: current.cached === true,
    cachedAt: current.cached_at,
  };
}

// staleTimeFor 是新解度策略，单独拆出来以便脱离 QueryClient 做单元测试。
//
// 缓存作答立即视为过期。服务器最多提供 `modelCatalogServeWindow` 旧的快照，并排队
// 自己的后台刷新；若客户端也把该响应当成几分钟内新鲜，可观测的陈旧度就是"服务端
// 窗口 + 客户端窗口"（而且刷新后的目录永远到不了触发刷新的那个标签页）。返回 0
// 把上界单独钉在服务端窗口：下次挂载/聚焦会重新校验、取到刷新快照；因为查询已持有
// 数据，React Query 在后台重取——`isLoading` 保持 false，选择器不会闪一份空加载态
// （MUL-5444）。
export function staleTimeFor(data: RuntimeModelsResult | undefined): number {
  if (!data) return 0;
  return data.cached ? 0 : LIVE_MODELS_STALE_TIME_MS;
}

/** 某 runtime 的模型目录查询配置（发起异步发现并轮询）。 */
export function runtimeModelsOptions(runtimeId: string | null | undefined) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeModelsKeys.forRuntime(runtimeId)
      : runtimeModelsKeys.all(),
    queryFn: () => resolveRuntimeModels(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: (query) => staleTimeFor(query.state.data),
    gcTime: MODELS_GC_TIME_MS,
    retry: false,
  });
}
