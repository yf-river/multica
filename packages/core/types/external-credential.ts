// 本文件定义「外部凭证（External Credential）」相关类型：把某个第三方平台
//（目前是 TAPD / 工蜂）的访问凭证存成可复用配置，供 agent 调用外部服务。

/** 受支持的外部凭证提供方。 */
export type ExternalCredentialProvider = "tapd" | "gongfeng";

/** 凭证的校验状态。 */
type ExternalCredentialStatus =
  | "unverified"
  | "verified"
  | "failed"
  | "disabled";

/** 凭证秘密的存放方式：具体秘密本身不回读，只有是否已配置的信息。 */
interface ExternalCredentialSecretBinding {
  configured: boolean;
  mode: "secret_ref" | "encrypted_secret" | "missing" | (string & {});
  hint?: string;
}

/** 一条外部凭证配置（对某平台的一组访问密钥）。 */
export interface ExternalCredentialProfile {
  id: string;
  provider: ExternalCredentialProvider | (string & {});
  name: string;
  secret_binding: ExternalCredentialSecretBinding;
  status: ExternalCredentialStatus | (string & {});
  last_error?: string;
}

/** 新建外部凭证配置的请求体。 */
export interface CreateExternalCredentialProfileRequest {
  provider: ExternalCredentialProvider;
  name?: string;
  secret_ref?: string;
  token?: string;
  capabilities?: Record<string, unknown>;
  verify_now?: boolean;
}

/** 更新外部凭证配置的请求体（provider 不可改，其余字段按需传）。 */
export type UpdateExternalCredentialProfileRequest = Partial<
  Omit<CreateExternalCredentialProfileRequest, "provider">
> & { status?: ExternalCredentialStatus };

/** 测试外部凭证的请求体：只需 provider 与可用的秘密来源。 */
export type TestExternalCredentialProfileRequest = Pick<
  CreateExternalCredentialProfileRequest,
  "provider" | "secret_ref" | "token"
>;

/** 测试外部凭证的响应：测试通过/失败及错误信息。 */
export interface TestExternalCredentialProfileResponse {
  status: ExternalCredentialStatus | (string & {});
  last_error?: string;
}
