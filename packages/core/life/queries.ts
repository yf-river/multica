// 本文件封装 life 模块的 React Query 查询。life 模块承载「伴生智能体」的人格
// 数据：陪伴画像、记忆、提案、实验、编年史、主动检查、身份版本、关系事件、
// 素材、内心独白、话题、承诺、主动策略、观察者/观察席、模块、认知任务、升级评估等。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** life 模块全部查询的 query key 命名空间。 */
export const lifeKeys = {
  all: (wsId: string) => ["life", wsId] as const,
  companion: (wsId: string) => [...lifeKeys.all(wsId), "companion"] as const,
  memories: (wsId: string) => [...lifeKeys.all(wsId), "memories"] as const,
  proposals: (wsId: string) => [...lifeKeys.all(wsId), "proposals"] as const,
  experiments: (wsId: string) => [...lifeKeys.all(wsId), "experiments"] as const,
  chronicle: (wsId: string) => [...lifeKeys.all(wsId), "chronicle"] as const,
  proactiveChecks: (wsId: string) => [...lifeKeys.all(wsId), "proactive-checks"] as const,
	identity: (wsId: string) => [...lifeKeys.all(wsId), "identity"] as const,
	relationships: (wsId: string) => [...lifeKeys.all(wsId), "relationships"] as const,
	materials: (wsId: string) => [...lifeKeys.all(wsId), "materials"] as const,
	thoughts: (wsId: string) => [...lifeKeys.all(wsId), "internal-thoughts"] as const,
	topics: (wsId: string) => [...lifeKeys.all(wsId), "topics"] as const,
	commitments: (wsId: string) => [...lifeKeys.all(wsId), "commitments"] as const,
	policy: (wsId: string) => [...lifeKeys.all(wsId), "policy"] as const,
	observers: (wsId: string) => [...lifeKeys.all(wsId), "observers"] as const,
	observationSeat: (wsId: string) => [...lifeKeys.all(wsId), "observation-seat"] as const,
	modules: (wsId: string) => [...lifeKeys.all(wsId), "modules"] as const,
	jobs: (wsId: string) => [...lifeKeys.all(wsId), "jobs"] as const,
	upgrades: (wsId: string) => [...lifeKeys.all(wsId), "upgrades"] as const,
};

/** 查询当前陪伴画像（当前把哪个 agent 设为 companion）。 */
export const companionProfileOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.companion(wsId),
  queryFn: () => api.getCompanionProfile(),
});

/** 查询记忆列表。 */
export const lifeMemoryListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.memories(wsId),
  queryFn: () => api.listLifeMemories(),
});

/** 查询提案列表。 */
export const lifeProposalListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.proposals(wsId),
  queryFn: () => api.listLifeProposals(),
});

/** 查询实验列表。 */
export const lifeExperimentListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.experiments(wsId),
  queryFn: () => api.listLifeExperiments(),
});

/** 查询编年史列表。 */
export const lifeChronicleListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.chronicle(wsId),
  queryFn: () => api.listLifeChronicle(),
});

/** 查询主动检查列表。 */
export const lifeProactiveCheckListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.proactiveChecks(wsId),
  queryFn: () => api.listLifeProactiveChecks(),
});

/** 查询身份版本列表。 */
export const lifeIdentityListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.identity(wsId), queryFn: () => api.listLifeIdentityVersions() });
/** 查询关系事件列表。 */
export const lifeRelationshipListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.relationships(wsId), queryFn: () => api.listLifeRelationshipEvents() });
/** 查询素材列表。 */
export const lifeMaterialListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.materials(wsId), queryFn: () => api.listLifeMaterials() });
/** 查询内心独白列表。 */
export const lifeInternalThoughtListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.thoughts(wsId), queryFn: () => api.listLifeInternalThoughts() });
/** 查询话题列表。 */
export const lifeTopicListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.topics(wsId), queryFn: () => api.listLifeTopics() });
/** 查询承诺列表。 */
export const lifeCommitmentListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.commitments(wsId), queryFn: () => api.listLifeCommitments() });
/** 查询主动策略配置。 */
export const lifeProactivePolicyOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.policy(wsId), queryFn: () => api.getLifeProactivePolicy() });
/** 查询观察者列表。 */
export const lifeObserverListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.observers(wsId), queryFn: () => api.listLifeObservers() });
/** 查询观察席占用情况。 */
export const lifeObservationSeatOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.observationSeat(wsId), queryFn: () => api.listLifeObservationSeat() });
/** 查询模块列表。 */
export const lifeModuleListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.modules(wsId), queryFn: () => api.listLifeModules() });
/** 查询认知任务列表（15 秒轮询一次，因为任务会在后台推进）。 */
export const lifeCognitionJobListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.jobs(wsId), queryFn: () => api.listLifeCognitionJobs(), refetchInterval: 15_000 });
/** 查询升级评估列表。 */
export const lifeUpgradeEvaluationListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.upgrades(wsId), queryFn: () => api.listLifeUpgradeEvaluations() });
