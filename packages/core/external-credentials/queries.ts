import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateExternalCredentialProfileRequest,
  ExternalCredentialProfile,
  ExternalCredentialProvider,
  TestExternalCredentialProfileRequest,
  UpdateExternalCredentialProfileRequest,
} from "../types/external-credential";

export const externalCredentialKeys = {
  all: ["external-credential-profiles"] as const,
  list: (provider: ExternalCredentialProvider) =>
    [...externalCredentialKeys.all, provider] as const,
};

export function externalCredentialProfilesOptions(provider: ExternalCredentialProvider) {
  return queryOptions({
    queryKey: externalCredentialKeys.list(provider),
    queryFn: () => api.listExternalCredentialProfiles(provider),
  });
}

export function useCreateExternalCredentialProfile(provider: ExternalCredentialProvider) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateExternalCredentialProfileRequest) =>
      api.createExternalCredentialProfile(request),
    onSuccess: (profile) => client.setQueryData<ExternalCredentialProfile[]>(
      externalCredentialKeys.list(provider),
      (current) => [...(current ?? []).filter((item) => item.id !== profile.id), profile],
    ),
  });
}

export function useUpdateExternalCredentialProfile(provider: ExternalCredentialProvider) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, request }: { id: string; request: UpdateExternalCredentialProfileRequest }) =>
      api.updateExternalCredentialProfile(id, request),
    onSuccess: (profile) => client.setQueryData<ExternalCredentialProfile[]>(
      externalCredentialKeys.list(provider),
      (current) => (current ?? []).map((item) => item.id === profile.id ? profile : item),
    ),
  });
}

export function useDeleteExternalCredentialProfile(provider: ExternalCredentialProvider) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (profile: ExternalCredentialProfile) => api.deleteExternalCredentialProfile(profile.id),
    onSuccess: (_value, profile) => client.setQueryData<ExternalCredentialProfile[]>(
      externalCredentialKeys.list(provider),
      (current) => (current ?? []).filter((item) => item.id !== profile.id),
    ),
  });
}

export function useTestExternalCredentialProfile() {
  return useMutation({
    mutationFn: (request: TestExternalCredentialProfileRequest) =>
      api.testExternalCredentialProfile(request),
  });
}
