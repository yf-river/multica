import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export interface TestAgentPolicy {
  provider: "codebuddy" | "codex";
  model: string;
  thinking_level: string;
  service_tier: string;
}
export interface TestEnvironmentAgent {
  id: string;
  name: string;
  model: string | null;
  runtime_id: string | null;
  status: string;
  archived_at: string | null;
}
export interface TestEnvironmentControl {
  name: string;
  revision: number;
  desired: {
    policy: TestAgentPolicy;
    overrides: Record<string, TestAgentPolicy>;
  };
  observed: {
    status?: string;
    revision?: number;
    error?: string;
    agents?: TestEnvironmentAgent[];
    runtimes?: { id: string; provider: string }[];
  };
  observed_at: string | null;
}
export const testAgentPolicies: Record<"codebuddy" | "codex", TestAgentPolicy> =
  {
    codebuddy: {
      provider: "codebuddy",
      model: "deepseek-v4.1-flash",
      thinking_level: "",
      service_tier: "",
    },
    codex: {
      provider: "codex",
      model: "gpt-6.1-sol",
      thinking_level: "low",
      service_tier: "default",
    },
  };
export const testAgentControlKey = (workspace: string) =>
  ["test-agent-control", workspace] as const;
export const testAgentControlOptions = (workspace: string) =>
  queryOptions({
    queryKey: testAgentControlKey(workspace),
    queryFn: () => api.getTestAgentControl(),
    refetchInterval: 30_000,
  });

export const testAgentControlCapabilityOptions = (workspace: string) =>
  queryOptions({
    queryKey: [...testAgentControlKey(workspace), "capability"],
    queryFn: () => api.getTestAgentControlCapability(),
    refetchInterval: 30_000,
  });
