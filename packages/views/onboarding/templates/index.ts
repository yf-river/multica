export {
  HELPER_INSTRUCTIONS,
  HELPER_DESCRIPTION,
  type HelperInstructionsLang,
} from "./helper-instructions";
export {
  INSTALL_RUNTIME_ISSUE_TITLE,
  INSTALL_RUNTIME_ISSUE_BODY,
  FOLLOWUP_COMMENT_PREFIX,
} from "./install-runtime-issue";
export {
  CREATE_AGENT_GUIDE_ISSUE_TITLE,
  getCreateAgentGuideBody,
} from "./create-agent-guide-issue";
export {
  HELPER_STARTER_PROMPTS,
  STARTER_CARD_IDS,
  type StarterCardId,
} from "./helper-starter-prompts";
export {
  buildUserContextSection,
  type UserContextLabels,
  type QuestionnaireRaw,
} from "./user-context";

type ContentLang = "zh";

/**
 * Onboarding starter content is Chinese-only after the locale-system cleanup.
 */
export function pickContentLang(
  _language?: string | null,
): ContentLang {
  return "zh";
}
