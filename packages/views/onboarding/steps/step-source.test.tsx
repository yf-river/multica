import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { QuestionnaireAnswers } from "@multica/core/onboarding";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";
import { StepSource } from "./step-source";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, onboarding: enOnboarding } };

const EMPTY: QuestionnaireAnswers = {
  source: [],
  source_other: null,
  source_skipped: false,
  role: null,
  role_other: null,
  role_skipped: false,
  use_case: [],
  use_case_other: null,
  use_case_skipped: false,
  version: 2,
};

function renderStep(answers: QuestionnaireAnswers = EMPTY) {
  const onChange = vi.fn();
  const onAdvance = vi.fn();
  const onSkip = vi.fn();
  const onBack = vi.fn();
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <StepSource
        answers={answers}
        onChange={onChange}
        onAdvance={onAdvance}
        onSkip={onSkip}
        onBack={onBack}
      />
    </I18nProvider>,
  );
  return { onChange, onAdvance, onSkip, onBack };
}

describe("StepSource（单选主要来源）", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("点击非其他选项会写入单元素 source 数组", async () => {
    const user = userEvent.setup();
    const { onChange, onAdvance } = renderStep();

    await user.click(screen.getByRole("radio", { name: /linkedin/i }));

    expect(onChange).toHaveBeenCalledWith({
      source: ["social_linkedin"],
      source_other: null,
      source_skipped: false,
    });
    // A click only records — it must NOT auto-advance.
    expect(onAdvance).not.toHaveBeenCalled();
  });

  it("选择第二个选项会替换第一个，不会叠加", async () => {
    const user = userEvent.setup();
    const { onChange } = renderStep({
      ...EMPTY,
      source: ["social_linkedin"],
    });

    await user.click(screen.getByRole("radio", { name: /twitter/i }));

    expect(onChange).toHaveBeenCalledWith({
      source: ["social_x"],
      source_other: null,
      source_skipped: false,
    });
  });

  it("跳过会清空 source 和 source_other，标记跳过并调用 onSkip", async () => {
    const user = userEvent.setup();
    const { onChange, onSkip } = renderStep();

    await user.click(screen.getByRole("button", { name: /跳过/ }));

    expect(onChange).toHaveBeenCalledWith({
      source: [],
      source_other: null,
      source_skipped: true,
    });
    expect(onSkip).toHaveBeenCalledTimes(1);
  });

  it("点击其他会写入 source: ['other'] 并允许输入 source_other", async () => {
    const user = userEvent.setup();
    const { onChange } = renderStep();

    await user.click(screen.getByRole("radio", { name: /^其他$/ }));

    expect(onChange).toHaveBeenCalledWith({
      source: ["other"],
      source_other: null,
      source_skipped: false,
    });

    const input = await screen.findByPlaceholderText(/播客/);
    await user.type(input, "x");
    expect(onChange).toHaveBeenLastCalledWith({ source_other: "x" });
  });

  it("从其他切走会清空 source_other，避免旧值泄漏", async () => {
    const user = userEvent.setup();
    const { onChange } = renderStep({
      ...EMPTY,
      source: ["other"],
      source_other: "a podcast",
    });

    await user.click(screen.getByRole("radio", { name: /linkedin/i }));

    expect(onChange).toHaveBeenCalledWith({
      source: ["social_linkedin"],
      source_other: null,
      source_skipped: false,
    });
  });
});
