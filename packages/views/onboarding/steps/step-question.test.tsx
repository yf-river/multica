import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";
import { StepQuestion, type QuestionOption } from "./step-question";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, onboarding: enOnboarding } };

const OPTIONS: readonly QuestionOption[] = [
  { slug: "a", icon: <span>A</span>, label: "选项甲" },
  { slug: "b", icon: <span>B</span>, label: "选项乙" },
  { slug: "other", icon: <span>O</span>, label: "其他", isOther: true },
];

function renderShell(overrides: Partial<React.ComponentProps<typeof StepQuestion>> = {}) {
  const onAnswer = vi.fn();
  const onAdvance = vi.fn();
  const onSkip = vi.fn();
  const onBack = vi.fn();
  const onOtherChange = vi.fn();
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <StepQuestion
        step="source"
        number={1}
        question="测试问题"
        options={OPTIONS}
        selectedSlugs={[]}
        otherValue=""
        onOtherChange={onOtherChange}
        otherPlaceholder="在这里输入"
        onAnswer={onAnswer}
        onAdvance={onAdvance}
        onSkip={onSkip}
        onBack={onBack}
        {...overrides}
      />
    </I18nProvider>,
  );
  return { onAnswer, onAdvance, onSkip, onBack, onOtherChange };
}

describe("StepQuestion", () => {
  beforeEach(() => vi.restoreAllMocks());

  it("未选择任何内容时继续按钮禁用", () => {
    renderShell();
    const continueBtn = screen.getByRole("button", { name: /继续/ });
    expect(continueBtn).toBeDisabled();
  });

  it("点击非其他选项会记录 slug，但不会自动前进", async () => {
    const user = userEvent.setup();
    const { onAnswer, onAdvance } = renderShell();
    await user.click(screen.getByRole("radio", { name: /选项甲/ }));
    expect(onAnswer).toHaveBeenCalledWith("a");
    expect(onAdvance).not.toHaveBeenCalled();
  });

  it("选中非其他 slug 时继续按钮启用并触发 onAdvance", async () => {
    const user = userEvent.setup();
    const { onAdvance } = renderShell({ selectedSlugs: ["a"] });
    const continueBtn = screen.getByRole("button", { name: /继续/ });
    expect(continueBtn).toBeEnabled();
    await user.click(continueBtn);
    expect(onAdvance).toHaveBeenCalledTimes(1);
  });

  it("选择其他且输入为空时继续按钮保持禁用", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.click(screen.getByRole("radio", { name: /^其他$/ }));
    // pendingOther is now true; Continue must remain disabled until the
    // free-text input has content.
    expect(screen.getByRole("button", { name: /继续/ })).toBeDisabled();
  });

  it("选择其他且 otherValue 非空时继续按钮启用", async () => {
    const user = userEvent.setup();
    const { onAdvance } = renderShell({
      selectedSlugs: ["other"],
      otherValue: "hello",
    });
    const continueBtn = screen.getByRole("button", { name: /继续/ });
    expect(continueBtn).toBeEnabled();
    await user.click(continueBtn);
    expect(onAdvance).toHaveBeenCalledTimes(1);
  });

  it("选择其他且 otherValue 只有空白时继续按钮禁用", () => {
    renderShell({ selectedSlugs: ["other"], otherValue: "   " });
    expect(screen.getByRole("button", { name: /继续/ })).toBeDisabled();
  });

  it("跳过按钮始终启用并触发 onSkip", async () => {
    const user = userEvent.setup();
    const { onSkip } = renderShell();
    await user.click(screen.getByRole("button", { name: /^跳过$/ }));
    expect(onSkip).toHaveBeenCalledTimes(1);
  });

  it("仅提供 onBack 时渲染返回按钮", () => {
    const { unmount } = render(
      <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
        <StepQuestion
          step="source"
          number={1}
          question="测试"
          options={OPTIONS}
          selectedSlugs={[]}
          otherValue=""
          onOtherChange={vi.fn()}
          otherPlaceholder="输入"
          onAnswer={vi.fn()}
          onAdvance={vi.fn()}
          onSkip={vi.fn()}
        />
      </I18nProvider>,
    );
    expect(screen.queryByRole("button", { name: /^返回$/ })).not.toBeInTheDocument();
    unmount();
  });
});
