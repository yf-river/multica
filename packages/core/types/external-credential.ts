export type ExternalCredentialProvider = "tapd" | "gongfeng";

export type ExternalCredentialStatus =
  | "unverified"
  | "verified"
  | "failed"
  | "disabled";

export interface ExternalCredentialSecretBinding {
  configured: boolean;
  redacted: boolean;
  mode: "secret_ref" | "encrypted_secret" | "missing" | (string & {});
  hint?: string;
}

export interface ExternalCredentialProfile {
  id: string;
  user_id: string;
  scope: "account" | (string & {});
  provider: ExternalCredentialProvider | (string & {});
  name: string;
  secret_binding: ExternalCredentialSecretBinding;
  capabilities: Record<string, unknown>;
  status: ExternalCredentialStatus | (string & {});
  last_verified_at: string | null;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface ListExternalCredentialProfilesResponse {
  profiles: ExternalCredentialProfile[];
}

export interface CreateExternalCredentialProfileRequest {
  provider: ExternalCredentialProvider;
  name?: string;
  secret_ref?: string;
  token?: string;
  capabilities?: Record<string, unknown>;
  verify_now?: boolean;
}

export interface UpdateExternalCredentialProfileRequest {
  name?: string;
  secret_ref?: string;
  token?: string;
  capabilities?: Record<string, unknown>;
  status?: ExternalCredentialStatus;
  last_error?: string;
  verify_now?: boolean;
}
