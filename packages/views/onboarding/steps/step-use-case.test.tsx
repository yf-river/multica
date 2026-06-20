import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { QuestionnaireAnswers } from "@multica/core/onboarding";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";
import { StepUseCase } from "./step-use-case";

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
      <StepUseCase
        answers={answers}
        onChange={onChange}
        onAdvance={onAdvance}
        onSkip={onSkip}
      />
    </I18nProvider>,
  );
  return { onChange, onAdvance, onSkip };
}

describe("StepUseCase（多选）", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("点击使用场景会追加到数组", async () => {
    const user = userEvent.setup();
    const { onChange, onAdvance } = renderStep();

    await user.click(screen.getByRole("checkbox", { name: /写代码/ }));

    expect(onChange).toHaveBeenCalledWith({
      use_case: ["ship_code"],
      use_case_skipped: false,
    });
    expect(onAdvance).not.toHaveBeenCalled();
  });

  it("点击已选使用场景会移除它", async () => {
    const user = userEvent.setup();
    const { onChange } = renderStep({ ...EMPTY, use_case: ["ship_code"] });

    await user.click(screen.getByRole("checkbox", { name: /写代码/ }));

    expect(onChange).toHaveBeenCalledWith({
      use_case: [],
      use_case_skipped: false,
    });
  });

  it("跳过会清空字段并标记 use_case_skipped", async () => {
    const user = userEvent.setup();
    const { onChange, onSkip } = renderStep();

    await user.click(screen.getByRole("button", { name: /跳过/ }));

    expect(onChange).toHaveBeenCalledWith({
      use_case: [],
      use_case_other: null,
      use_case_skipped: true,
    });
    expect(onSkip).toHaveBeenCalledTimes(1);
  });

  it("选择其他并输入时通过 onChange 写入 use_case_other", async () => {
    const user = userEvent.setup();
    const { onChange } = renderStep();

    await user.click(screen.getByRole("checkbox", { name: /^其他$/ }));
    expect(onChange).toHaveBeenCalledWith({
      use_case: ["other"],
      use_case_skipped: false,
    });

    const input = await screen.findByPlaceholderText(/学习小组/);
    await user.type(input, "z");
    expect(onChange).toHaveBeenLastCalledWith({ use_case_other: "z" });
  });
});
