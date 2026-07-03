import type { ComponentType, ReactElement } from "react";
import { render } from "@testing-library/react";
import { vi } from "vitest";
import type { QuestionnaireAnswers } from "@multica/core/onboarding";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/zh-Hans/common.json";
import enOnboarding from "../../locales/zh-Hans/onboarding.json";

const TEST_RESOURCES = { "zh-Hans": { common: enCommon, onboarding: enOnboarding } };

export const EMPTY_QUESTIONNAIRE_ANSWERS: QuestionnaireAnswers = {
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

type QuestionnaireStepProps = {
  answers: QuestionnaireAnswers;
  onChange: (patch: Partial<QuestionnaireAnswers>) => void;
  onAdvance: () => void;
  onSkip: () => void;
};

type StepCallbacks = {
  onChange: (patch: Partial<QuestionnaireAnswers>) => void;
  onAdvance: () => void;
  onSkip: () => void;
};

function renderWithOnboardingI18n(ui: ReactElement): void {
  render(
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      {ui}
    </I18nProvider>,
  );
}

export function renderQuestionnaireStep(
  Step: ComponentType<QuestionnaireStepProps>,
  answers: QuestionnaireAnswers = EMPTY_QUESTIONNAIRE_ANSWERS,
): StepCallbacks {
  const onChange = vi.fn();
  const onAdvance = vi.fn();
  const onSkip = vi.fn();
  renderWithOnboardingI18n(
    <Step
      answers={answers}
      onChange={onChange}
      onAdvance={onAdvance}
      onSkip={onSkip}
    />,
  );
  return { onChange, onAdvance, onSkip };
}
