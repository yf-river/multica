// @vitest-environment node
import type { TFunction } from "i18next";
import { createI18n } from "@multica/core/i18n/react";
import type { SupportedLocale } from "@multica/core/i18n";
import { describe, expect, it } from "vitest";
import zhHansAgents from "../../../locales/zh-Hans/agents.json";

import {
  FAILURE_REASON_I18N_KEYS,
  cancelReasonLabel,
  failureReasonLabel,
} from "./task-failure";

function fixedT(locale: SupportedLocale = "zh-Hans"): TFunction<"agents"> {
  const resources = { "zh-Hans": { agents: zhHansAgents } };
  const i18n = createI18n(locale, resources);
  return i18n.getFixedT(locale, "agents") as TFunction<"agents">;
}

const zhT = fixedT();

// cancelReasonLabel decides which cancelled rows explain themselves. The rule
// it must hold: a SERVER-cancelled row (persisted reason) reads like a failed
// row, a user's own cancel stays a plain "Cancelled" — labelling every cancel
// would bury the rows that actually need the user to act.
describe("cancelReasonLabel", () => {
  it("returns null for a user-initiated cancel", () => {
    expect(
      cancelReasonLabel(
        { status: "cancelled", error: null, failure_reason: null },
        zhT,
      ),
    ).toBeNull();
  });

  it("only ever labels cancelled rows — failed rows have their own path", () => {
    expect(
      cancelReasonLabel(
        { status: "failed", error: "boom", failure_reason: "timeout" },
        zhT,
      ),
    ).toBeNull();
  });

  it("labels the worktree claim gate's cancellation", () => {
    expect(
      cancelReasonLabel(
        {
          status: "cancelled",
          error: "worktree mode needs daemon version 0.4.24 or newer",
          failure_reason: "local_directory_error",
        },
        zhT,
      ),
    ).toBe("本地目录出错");
  });

  it("localizes a generic system cancellation", () => {
    expect(
      cancelReasonLabel(
        {
          status: "cancelled",
          error: "work preserved in the worktree at /env/worktree",
          failure_reason: null,
        },
        zhT,
      ),
    ).toBe("系统已取消");
  });
});

describe("failureReasonLabel", () => {
  it("renders every known reason through Chinese resources", () => {
    for (const reason of Object.keys(FAILURE_REASON_I18N_KEYS)) {
      const label = failureReasonLabel(reason, zhT);
      expect(label, reason).not.toBe(reason);
      expect(label, reason).not.toContain("task_failure.");
      expect(label, reason).not.toBe("");
    }
  });

  it("covers platform reasons added after the original hardcoded map", () => {
    expect(failureReasonLabel("invalid_task_identity", zhT)).toBe(
      "任务身份不匹配",
    );
  });

  it("covers operational reasons emitted outside the canonical taxonomy", () => {
    expect(failureReasonLabel("agent_fallback_message", zhT)).toBe(
      "智能体返回了兜底消息",
    );
    expect(failureReasonLabel("idle_watchdog", zhT)).toBe(
      "智能体长时间无活动，运行已停止",
    );
    expect(failureReasonLabel("cancelled", zhT)).toBe(
      "系统已取消",
    );
  });

  it("localizes the legacy user cancellation value retained by the service", () => {
    expect(failureReasonLabel("user_cancelled", zhT)).toBe(
      "用户取消",
    );
  });

  it("uses native copy for representative refined reasons", () => {
    expect(
      failureReasonLabel(
        "agent_error.provider_quota_limit",
        fixedT("zh-Hans"),
      ),
    ).toBe("提供商配额已用尽");
    expect(failureReasonLabel("agent_error.context_overflow", zhT)).toBe(
      "上下文窗口已超限",
    );
    expect(failureReasonLabel("agent_error.missing_config", zhT)).toBe(
      "缺少 API 密钥或配置",
    );
  });

  it("still falls back to the raw wire value for unknown reasons", () => {
    expect(failureReasonLabel("brand_new_reason", zhT)).toBe("brand_new_reason");
  });

  it.each(["constructor", "toString", "__proto__"])(
    "treats inherited Object property %s as an unknown wire value",
    (reason) => {
      expect(failureReasonLabel(reason, zhT)).toBe(reason);
    },
  );

  it("returns null when the server supplied no reason", () => {
    expect(failureReasonLabel(null, zhT)).toBeNull();
    expect(failureReasonLabel(undefined, zhT)).toBeNull();
    expect(failureReasonLabel("", zhT)).toBeNull();
  });
});
