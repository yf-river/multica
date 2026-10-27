import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export interface TestAgentPolicy {
  provider: string;
  /** 承载该模型的运行时 provider；旧配置没有该字段时回落到 provider。 */
  runtime?: string;
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
    runtimes?: {
      id: string;
      provider: string;
      /** 发布器回写的真实模型目录；缺失时前端回退到运行时接口或内置预设。 */
      models?: {
        id: string;
        label: string;
        thinking?: { supported_levels?: { value: string; label: string }[] };
      }[];
    }[];
  };
  observed_at: string | null;
}
export const testAgentPolicies: Record<"codebuddy" | "codex" | "dsh", TestAgentPolicy> = {
  dsh: {
    provider: "dsh",
    runtime: "dsh",
    model: "codebuddy/deepseek-v4.1-flash",
    thinking_level: "",
    service_tier: "",
  },
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
