import { useT } from "../../i18n";
import { ExternalCredentialPanel } from "./external-credential-panel";

export function TokensTab() {
  const { t } = useT("settings");
  return (
    <div className="space-y-6">
      <section className="space-y-1">
        <h2 className="text-sm font-semibold">
          {t(($) => $.tokens.external_credentials_title)}
        </h2>
        <p className="max-w-3xl text-sm text-muted-foreground">
          {t(($) => $.tokens.external_credentials_description)}
        </p>
      </section>
      <ExternalCredentialPanel
        config={{
          provider: "gongfeng",
          testId: "settings-gongfeng-credential-panel",
          title: t(($) => $.tokens.gongfeng_credential.title),
          description: t(($) => $.tokens.gongfeng_credential.description),
          defaultName: "gongfeng-default",
          mcpServer: "gongfeng",
          defaultEnvName: "GONGFENG_PRIVATE_TOKEN",
          tokenPlaceholder: "粘贴工蜂 access token",
          replaceTokenPlaceholder: "输入新 token 可替换当前凭据",
          emptyTokenMessage: "请输入工蜂访问令牌",
          savedToast: "工蜂凭据已保存",
          saveErrorToast: "保存工蜂凭据失败",
          unavailableToast: "工蜂凭据不可用",
          removedToast: "工蜂凭据已移除",
          removeErrorToast: "移除工蜂凭据失败",
          testSuccessToast: "工蜂连接测试完成",
          testErrorToast: "测试工蜂连接失败",
        }}
      />
      <ExternalCredentialPanel
        config={{
          provider: "tapd",
          testId: "settings-tapd-credential-panel",
          title: t(($) => $.tokens.tapd_credential.title),
          description: t(($) => $.tokens.tapd_credential.description),
          defaultName: "tapd-default",
          mcpServer: "mcp-server-tapd",
          defaultEnvName: "TAPD_ACCESS_TOKEN",
          tokenPlaceholder: "粘贴 TAPD access token",
          replaceTokenPlaceholder: "输入新 token 可替换当前凭据",
          emptyTokenMessage: "请输入 TAPD 访问令牌",
          savedToast: "TAPD 凭据已保存",
          saveErrorToast: "保存 TAPD 凭据失败",
          unavailableToast: "TAPD 凭据不可用",
          removedToast: "TAPD 凭据已移除",
          removeErrorToast: "移除 TAPD 凭据失败",
          testSuccessToast: "TAPD 连接测试完成",
          testErrorToast: "测试 TAPD 连接失败",
        }}
      />
    </div>
  );
}
