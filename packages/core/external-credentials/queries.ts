import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateExternalCredentialProfileRequest,
  ExternalCredentialProvider,
  ExternalCredentialProfile,
  ListExternalCredentialProfilesResponse,
  UpdateExternalCredentialProfileRequest,
} from "../types";

export const externalCredentialProfileKeys = {
  all: ["external-credential-profiles"] as const,
  list: (provider?: ExternalCredentialProvider) =>
    provider
      ? ([...externalCredentialProfileKeys.all, "list", provider] as const)
      : ([...externalCredentialProfileKeys.all, "list"] as const),
};

export function externalCredentialProfilesOptions(provider?: ExternalCredentialProvider) {
  return queryOptions({
    queryKey: externalCredentialProfileKeys.list(provider),
    queryFn: () => api.listExternalCredentialProfiles(provider),
  });
}

export function useCreateExternalCredentialProfile(provider?: ExternalCredentialProvider) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateExternalCredentialProfileRequest) =>
      api.createExternalCredentialProfile(data),
    onSuccess: (created) => {
      const keyProvider = provider ?? (created.provider as ExternalCredentialProvider);
      qc.setQueryData<ListExternalCredentialProfilesResponse>(
        externalCredentialProfileKeys.list(keyProvider),
        (old) =>
          old
            ? {
                profiles: [...old.profiles.filter((p) => p.id !== created.id), created],
              }
            : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: externalCredentialProfileKeys.all });
    },
  });
}

export function useUpdateExternalCredentialProfile(provider?: ExternalCredentialProvider) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      data,
    }: {
      id: string;
      data: UpdateExternalCredentialProfileRequest;
    }) => api.updateExternalCredentialProfile(id, data),
    onSuccess: (updated) => {
      const keyProvider = provider ?? (updated.provider as ExternalCredentialProvider);
      qc.setQueryData<ListExternalCredentialProfilesResponse>(
        externalCredentialProfileKeys.list(keyProvider),
        (old) =>
          old
            ? {
                profiles: old.profiles.map((p) => (p.id === updated.id ? updated : p)),
              }
            : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: externalCredentialProfileKeys.all });
    },
  });
}

export function useDeleteExternalCredentialProfile(provider?: ExternalCredentialProvider) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (profile: ExternalCredentialProfile) =>
      api.deleteExternalCredentialProfile(profile.id),
    onSuccess: (_deleted, profile) => {
      const keyProvider = provider ?? (profile.provider as ExternalCredentialProvider);
      qc.setQueryData<ListExternalCredentialProfilesResponse>(
        externalCredentialProfileKeys.list(keyProvider),
        (old) =>
          old
            ? {
                profiles: old.profiles.filter((p) => p.id !== profile.id),
              }
            : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: externalCredentialProfileKeys.all });
    },
  });
}
