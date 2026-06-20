import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { QuestionnaireAnswers } from "@multica/core/onboarding";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";
import { StepRole } from "./step-role";

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
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <StepRole
        answers={answers}
        onChange={onChange}
        onAdvance={onAdvance}
        onSkip={onSkip}
      />
    </I18nProvider>,
  );
  return { onChange, onAdvance, onSkip };
}

describe("StepRole", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("选择角色会写入 slug 并清空其他/跳过状态", async () => {
    const user = userEvent.setup();
    const { onChange, onAdvance } = renderStep();

    await user.click(screen.getByRole("radio", { name: /工程师/ }));

    expect(onChange).toHaveBeenCalledWith({
      role: "engineer",
      role_other: null,
      role_skipped: false,
    });
    expect(onAdvance).not.toHaveBeenCalled();
  });

  it("跳过会清空字段并标记 role_skipped", async () => {
    const user = userEvent.setup();
    const { onChange, onSkip } = renderStep();

    await user.click(screen.getByRole("button", { name: /跳过/ }));

    expect(onChange).toHaveBeenCalledWith({
      role: null,
      role_other: null,
      role_skipped: true,
    });
    expect(onSkip).toHaveBeenCalledTimes(1);
  });

  it("选择其他并输入时通过 onChange 写入 role_other", async () => {
    const user = userEvent.setup();
    const { onChange } = renderStep();

    await user.click(screen.getByRole("radio", { name: /^其他$/ }));
    expect(onChange).toHaveBeenCalledWith({
      role: "other",
      role_skipped: false,
    });

    const input = await screen.findByPlaceholderText(/教师/);
    await user.type(input, "y");
    expect(onChange).toHaveBeenLastCalledWith({ role_other: "y" });
  });
});
