import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateExternalCredentialProfileRequest,
  ExternalCredentialProvider,
  ExternalCredentialProfile,
  TestExternalCredentialProfileRequest,
  TestExternalCredentialProfileResponse,
  UpdateExternalCredentialProfileRequest,
} from "../types";
import { createExternalCredentialProfileWithRecovery } from "./create-operation";

const externalCredentialProfileKeys = {
  all: ["external-credential-profiles"] as const,
  list: (provider: ExternalCredentialProvider) =>
    [...externalCredentialProfileKeys.all, "list", provider] as const,
};

export function externalCredentialProfilesOptions(provider: ExternalCredentialProvider) {
  return queryOptions({
    queryKey: externalCredentialProfileKeys.list(provider),
    queryFn: () => api.listExternalCredentialProfiles(provider),
  });
}

export function useCreateExternalCredentialProfile(provider: ExternalCredentialProvider) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateExternalCredentialProfileRequest) =>
      createExternalCredentialProfileWithRecovery(data),
    onSuccess: (created) => {
      qc.setQueryData<ExternalCredentialProfile[]>(
        externalCredentialProfileKeys.list(provider),
        (old) =>
          old ? [...old.filter((p) => p.id !== created.id), created] : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: externalCredentialProfileKeys.all });
    },
  });
}

export function useUpdateExternalCredentialProfile(provider: ExternalCredentialProvider) {
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
      qc.setQueryData<ExternalCredentialProfile[]>(
        externalCredentialProfileKeys.list(provider),
        (old) =>
          old ? old.map((p) => (p.id === updated.id ? updated : p)) : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: externalCredentialProfileKeys.all });
    },
  });
}

export function useDeleteExternalCredentialProfile(provider: ExternalCredentialProvider) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (profile: ExternalCredentialProfile) =>
      api.deleteExternalCredentialProfile(profile.id),
    onSuccess: (_deleted, profile) => {
      qc.setQueryData<ExternalCredentialProfile[]>(
        externalCredentialProfileKeys.list(provider),
        (old) =>
          old ? old.filter((p) => p.id !== profile.id) : old,
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: externalCredentialProfileKeys.all });
    },
  });
}

export function useTestExternalCredentialProfile() {
  return useMutation<TestExternalCredentialProfileResponse, Error, TestExternalCredentialProfileRequest>({
    mutationFn: (data) => api.testExternalCredentialProfile(data),
  });
}
