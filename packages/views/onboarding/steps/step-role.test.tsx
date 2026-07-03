import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EMPTY_QUESTIONNAIRE_ANSWERS as EMPTY, renderQuestionnaireStep } from "./test-helpers";
import { StepRole } from "./step-role";

const renderStep = (answers = EMPTY) => renderQuestionnaireStep(StepRole, answers);

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
