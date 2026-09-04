// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useTranslation } from "react-i18next";
import { I18nProvider } from "./provider";
import type { LocaleResources } from "./types";

afterEach(cleanup);

function DialogTitle() {
  const { t } = useTranslation("settings");
  return <span>{t("shortcuts.reset_confirm.title")}</span>;
}

describe("I18nProvider", () => {
  it("uses the Chinese resource selected at startup", () => {
    const resources: Record<string, LocaleResources> = {
      "zh-Hans": {
        settings: {
          shortcuts: {
            title: "快捷键",
            reset_confirm: { title: "恢复所有快捷键默认值？" },
          },
        },
      },
    };

    render(
      <I18nProvider locale="zh-Hans" resources={resources}>
        <DialogTitle />
      </I18nProvider>,
    );
    expect(screen.queryByText("恢复所有快捷键默认值？")).not.toBeNull();
  });
});
