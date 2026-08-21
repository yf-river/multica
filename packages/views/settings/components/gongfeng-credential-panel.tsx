"use client";

import { useT } from "../../i18n";
import { ExternalCredentialPanel } from "./external-credential-panel";

export function GongfengCredentialPanel() {
  const { t } = useT("settings");
  return (
    <ExternalCredentialPanel
      config={{
        provider: "gongfeng",
        testId: "settings-gongfeng-credential-panel",
        title: t(($) => $.tokens.gongfeng_credential.title),
        description: t(($) => $.tokens.gongfeng_credential.description),
        defaultName: "gongfeng-default",
        mcpServer: "gongfeng",
        defaultEnvName: "GONGFENG_ACCESS_TOKEN",
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
  );
}
