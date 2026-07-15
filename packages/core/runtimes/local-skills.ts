import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateRuntimeLocalSkillImportRequest,
  RuntimeLocalSkillImportResult,
  RuntimeLocalSkillsResult,
} from "../types";
import { pollRuntimeRequest } from "./poll-request";

const runtimeLocalSkillsRootKey = ["runtimes", "local-skills"] as const;

export const runtimeLocalSkillsKeys = {
  forRuntime: (runtimeId: string) =>
    [...runtimeLocalSkillsRootKey, runtimeId] as const,
};

const POLL_TIMEOUT_MS = 30_000;
// The server claims one full UI concurrency window per heartbeat, then allows
// 60 seconds for execution. Two minutes covers both phases with retry margin.
const IMPORT_POLL_TIMEOUT_MS = 2 * 60_000;

async function resolveRuntimeLocalSkills(
  runtimeId: string,
): Promise<RuntimeLocalSkillsResult> {
  const initial = await api.initiateListLocalSkills(runtimeId);
  const current = await pollRuntimeRequest(
    initial,
    (requestId) => api.getListLocalSkillsResult(runtimeId, requestId),
    POLL_TIMEOUT_MS,
    "runtime local skill discovery timed out",
  );

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "runtime local skill discovery failed");
  }

  return {
    skills: current.skills ?? [],
    supported: current.supported,
  };
}

export async function resolveRuntimeLocalSkillImport(
  runtimeId: string,
  payload: CreateRuntimeLocalSkillImportRequest,
): Promise<RuntimeLocalSkillImportResult> {
  const initial = await api.initiateImportLocalSkill(runtimeId, payload);
  const current = await pollRuntimeRequest(
    initial,
    (requestId) => api.getImportLocalSkillResult(runtimeId, requestId),
    IMPORT_POLL_TIMEOUT_MS,
    "runtime local skill import timed out",
  );

  if (current.status === "conflict") {
    if (!current.conflict) {
      throw new Error("runtime local skill import conflict missing details");
    }
    return {
      status: "conflict",
      conflict: current.conflict,
    };
  }

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "runtime local skill import failed");
  }
  if (!current.skill) {
    throw new Error("runtime local skill import did not return a skill");
  }

  return {
    status: current.action === "overwrite" ? "updated" : "created",
    skill: current.skill,
  };
}

export function runtimeLocalSkillsOptions(runtimeId: string | null | undefined) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeLocalSkillsKeys.forRuntime(runtimeId)
      : runtimeLocalSkillsRootKey,
    queryFn: () => resolveRuntimeLocalSkills(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: 30_000,
    retry: false,
  });
}
