// 本文件是 onboarding（新用户引导）模块的对外出口（barrel）。

export type {
  OnboardingStep,
  OnboardingCompletionPath,
  QuestionnaireAnswers,
  Role,
  UseCase,
} from "./types";
export {
  saveQuestionnaire,
  completeOnboarding,
  joinCloudWaitlist,
} from "./store";
export { ONBOARDING_STEP_ORDER } from "./step-order";
export {
  bootstrapMika,
  useBootstrapMika,
  type BootstrapMikaInput,
  type BootstrapMikaResult,
} from "./use-bootstrap-mika";
export {
  useWelcomeStore,
  type WelcomeSignal,
} from "./welcome-store";
export {
  MIKA_SYSTEM_KEY,
  isMikaAgent,
  memberNeedsMikaSetup,
  workspaceNeedsMika,
} from "./mika";
export { useMikaConversation } from "./use-mika-conversation";
