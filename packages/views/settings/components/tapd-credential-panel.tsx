"use client";

import { useT } from "../../i18n";
import { ExternalCredentialPanel } from "./external-credential-panel";

export function TapdCredentialPanel() {
  const { t } = useT("settings");
  return (
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
  );
}
