import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";
import { CliInstallInstructions } from "./cli-install-instructions";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, onboarding: enOnboarding } };

const ligatureClasses = [
  "[font-variant-ligatures:none]",
  "[font-feature-settings:'liga'_0]",
];

describe("CliInstallInstructions", () => {
  it("disables font ligatures in CLI command code", () => {
    render(
      <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
        <CliInstallInstructions />
      </I18nProvider>,
    );

    expect(
      screen.getByText(
        "multica setup self-host --server-url http://localhost:3000 --app-url http://localhost:3000",
      ),
    ).toHaveClass(...ligatureClasses);
    expect(screen.getByText("1. 安装 Multica 命令行工具")).toBeInTheDocument();
    expect(screen.getByText("2. 连接本平台并启动守护进程")).toBeInTheDocument();
  });
});
