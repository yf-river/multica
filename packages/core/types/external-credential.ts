export type ExternalCredentialProvider = "tapd" | "gongfeng";

export type ExternalCredentialStatus =
  | "unverified"
  | "verified"
  | "failed"
  | "disabled";

export interface ExternalCredentialSecretBinding {
  configured: boolean;
  mode: "secret_ref" | "encrypted_secret" | "missing" | (string & {});
  hint?: string;
}

export interface ExternalCredentialProfile {
  id: string;
  provider: ExternalCredentialProvider | (string & {});
  name: string;
  secret_binding: ExternalCredentialSecretBinding;
  status: ExternalCredentialStatus | (string & {});
  last_error?: string;
}

export interface CreateExternalCredentialProfileRequest {
  provider: ExternalCredentialProvider;
  name?: string;
  secret_ref?: string;
  token?: string;
  capabilities?: Record<string, unknown>;
  verify_now?: boolean;
}

export type UpdateExternalCredentialProfileRequest = Partial<
  Omit<CreateExternalCredentialProfileRequest, "provider">
>;

export type TestExternalCredentialProfileRequest = Pick<
  CreateExternalCredentialProfileRequest,
  "provider" | "secret_ref" | "token"
>;

export interface TestExternalCredentialProfileResponse {
  status: ExternalCredentialStatus | (string & {});
  last_error?: string;
}
