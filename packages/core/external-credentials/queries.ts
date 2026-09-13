// 本文件封装「外部凭据配置文件」的 React Query 查询与变更：
// 查询某提供方下的凭据列表、创建/更新/删除配置文件、以及测试凭据是否可用。

import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateExternalCredentialProfileRequest,
  ExternalCredentialProfile,
  ExternalCredentialProvider,
  TestExternalCredentialProfileRequest,
  UpdateExternalCredentialProfileRequest,
} from "../types/external-credential";

/** 外部凭据配置文件相关查询的 query key 命名空间。 */
export const externalCredentialKeys = {
  all: ["external-credential-profiles"] as const,
  list: (provider: ExternalCredentialProvider) =>
    [...externalCredentialKeys.all, provider] as const,
};

/** 查询指定提供方下的全部外部凭据配置文件。 */
export function externalCredentialProfilesOptions(provider: ExternalCredentialProvider) {
  return queryOptions({
    queryKey: externalCredentialKeys.list(provider),
    queryFn: () => api.listExternalCredentialProfiles(provider),
  });
}

/** 创建一条外部凭据配置文件，成功后直接合并进本地缓存（按 id 去重）。 */
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

/** 更新一条外部凭据配置文件，成功后用返回值替换本地缓存里的对应项。 */
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

/** 删除一条外部凭据配置文件，成功后从本地缓存中移除对应项。 */
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

/** 测试一组凭据是否真正可用（例如能否连上外部服务）。 */
export function useTestExternalCredentialProfile() {
  return useMutation({
    mutationFn: (request: TestExternalCredentialProfileRequest) =>
      api.testExternalCredentialProfile(request),
  });
}
