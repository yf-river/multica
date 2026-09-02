import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

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

export const companionProfileOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.companion(wsId),
  queryFn: () => api.getCompanionProfile(),
});

export const lifeMemoryListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.memories(wsId),
  queryFn: () => api.listLifeMemories(),
});

export const lifeProposalListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.proposals(wsId),
  queryFn: () => api.listLifeProposals(),
});

export const lifeExperimentListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.experiments(wsId),
  queryFn: () => api.listLifeExperiments(),
});

export const lifeChronicleListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.chronicle(wsId),
  queryFn: () => api.listLifeChronicle(),
});

export const lifeProactiveCheckListOptions = (wsId: string) => queryOptions({
  queryKey: lifeKeys.proactiveChecks(wsId),
  queryFn: () => api.listLifeProactiveChecks(),
});

export const lifeIdentityListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.identity(wsId), queryFn: () => api.listLifeIdentityVersions() });
export const lifeRelationshipListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.relationships(wsId), queryFn: () => api.listLifeRelationshipEvents() });
export const lifeMaterialListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.materials(wsId), queryFn: () => api.listLifeMaterials() });
export const lifeInternalThoughtListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.thoughts(wsId), queryFn: () => api.listLifeInternalThoughts() });
export const lifeTopicListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.topics(wsId), queryFn: () => api.listLifeTopics() });
export const lifeCommitmentListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.commitments(wsId), queryFn: () => api.listLifeCommitments() });
export const lifeProactivePolicyOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.policy(wsId), queryFn: () => api.getLifeProactivePolicy() });
export const lifeObserverListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.observers(wsId), queryFn: () => api.listLifeObservers() });
export const lifeObservationSeatOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.observationSeat(wsId), queryFn: () => api.listLifeObservationSeat() });
export const lifeModuleListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.modules(wsId), queryFn: () => api.listLifeModules() });
export const lifeCognitionJobListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.jobs(wsId), queryFn: () => api.listLifeCognitionJobs(), refetchInterval: 15_000 });
export const lifeUpgradeEvaluationListOptions = (wsId: string) => queryOptions({ queryKey: lifeKeys.upgrades(wsId), queryFn: () => api.listLifeUpgradeEvaluations() });
