import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";
import { createI18n } from "@multica/core/i18n/react";
import zhAutopilots from "../../locales/zh-Hans/autopilots.json";
import { formatSchedulePartialFailureToast } from "./autopilot-dialog-toast";

// Contract test for the autopilot-dialog partial-success toast formatting.
//
// The dialog routes its partial-success branches through
// `formatSchedulePartialFailureToast`, so this test drives that exact
// helper rather than calling `t(...)` independently. That means a regression
// in either side — the JSON template (e.g. `{reason}` instead of `{{reason}}`)
// or the call-site variable name (e.g. `{ msg: ... }` instead of
// `{ reason: ... }`) — fails this test with the substring assertion.

describe("autopilot dialog partial-success toast", () => {
  const reason = "schedule conflict: 09:00 overlaps existing trigger";

  describe("zh-Hans", () => {
    const i18n = createI18n("zh-Hans", {
      "zh-Hans": { autopilots: zhAutopilots },
    });
    const t = i18n.getFixedT("zh-Hans", "autopilots") as TFunction<"autopilots">;

    it("renders create partial-success with the server reason verbatim", () => {
      const rendered = formatSchedulePartialFailureToast(t, "create", reason);
      expect(rendered).toContain(reason);
      expect(rendered).not.toContain("{{");
      expect(rendered).not.toContain("{reason}");
    });

    it("renders update partial-success with the server reason verbatim", () => {
      const rendered = formatSchedulePartialFailureToast(t, "update", reason);
      expect(rendered).toContain(reason);
      expect(rendered).not.toContain("{{");
      expect(rendered).not.toContain("{reason}");
    });

    it("falls back to the no-reason create string when reason is null", () => {
      expect(formatSchedulePartialFailureToast(t, "create", null)).toBe(
        "自动化已创建，但时间表保存失败",
      );
    });

    it("falls back to the no-reason update string when reason is null", () => {
      expect(formatSchedulePartialFailureToast(t, "update", null)).toBe(
        "自动化已更新，但时间表保存失败",
      );
    });
  });
});
