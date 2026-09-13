// onboarding（新用户引导）模块统一出口：对外提供引导主流程、CLI 安装指引、
// 云端等待列表与源回填弹窗等组件。
export {
  OnboardingFlow,
  type OnboardingDestination,
  type OnboardingStep,
} from "./onboarding-flow";
export { CliInstallInstructions } from "./steps/cli-install-instructions";
export { CloudWaitlistExpand } from "./components/cloud-waitlist-expand";
export { SourceBackfillModal } from "./source-backfill-modal";
