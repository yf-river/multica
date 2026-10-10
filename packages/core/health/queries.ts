import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "../auth";
import { api } from "../api";
import type { HealthObservation, HealthProfile } from "./schema";

export const healthKeys = {
  all: (wsId: string, userId: string) => ["health", wsId, userId] as const,
};
export function useHealthReportHistory(wsId: string, id: string) {
  const userId = useAuthStore((s) => s.user?.id) ?? "";
  return useQuery({
    queryKey: [...healthKeys.all(wsId, userId), "reportHistory", id],
    queryFn: () => api.healthReportHistory(id),
    enabled: !!wsId && !!userId && !!id,
  });
}
export function useHealthReportUpload(wsId: string) {
  const userId = useAuthStore((s) => s.user?.id) ?? "";
  const client = useQueryClient();
  return useMutation({
    mutationFn: (file: File) => api.uploadHealthReport(file),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: healthKeys.all(wsId, userId) }),
  });
}
export function useHealth(wsId: string) {
  const userId = useAuthStore((s) => s.user?.id) ?? "";
  const key = healthKeys.all(wsId, userId);
  const overview = useQuery({
    queryKey: [...key, "overview"],
    queryFn: () => api.healthOverview(),
    enabled: !!wsId && !!userId,
    refetchInterval: 5000,
  });
  const reports = useQuery({
    queryKey: [...key, "reports"],
    queryFn: () => api.healthReports(),
    enabled: !!wsId && !!userId,
    refetchInterval: 5000,
  });
  const revisions = useQuery({
    queryKey: [...key, "revisions"],
    queryFn: () => api.healthRevisions(),
    enabled: !!wsId && !!userId,
  });
  const client = useQueryClient();
  const invalidate = () => client.invalidateQueries({ queryKey: key });
  const profile = useMutation({
    mutationFn: ({
      value,
      revision,
    }: {
      value: HealthProfile;
      revision: number;
    }) => api.saveHealthProfile(value, revision),
    onSuccess: invalidate,
  });
  const observation = useMutation({
    mutationFn: ({
      value,
      revision,
    }: {
      value: Partial<HealthObservation>;
      revision: number;
    }) => api.saveHealthObservation(value, revision),
    onSuccess: invalidate,
  });
  const observations = useMutation({
    mutationFn: ({ values, revision }: { values: Partial<HealthObservation>[]; revision: number }) =>
      api.saveHealthObservations(values, revision),
    onSuccess: invalidate,
  });
  const upload = useMutation({
    mutationFn: (file: File) => api.uploadHealthReport(file),
    onSuccess: invalidate,
  });
  const process = useMutation({
    mutationFn: ({
      id,
      agentId,
      reprocess = false,
    }: {
      id: string;
      agentId: string;
      reprocess?: boolean;
    }) => api.processHealthReport(id, agentId, reprocess),
    onSuccess: invalidate,
  });
  const restore = useMutation({
    mutationFn: ({ target, revision }: { target: number; revision: number }) =>
      api.restoreHealthRevision(target, revision),
    onSuccess: invalidate,
  });
  const apply = useMutation({
    mutationFn: ({ id, revision }: { id: string; revision: number }) =>
      api.applyHealthReport(id, revision),
    onSuccess: invalidate,
  });
  return {
    overview,
    reports,
    revisions,
    profile,
    observation,
    observations,
    upload,
    process,
    restore,
    apply,
    invalidate,
  };
}
