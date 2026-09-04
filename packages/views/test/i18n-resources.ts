import type { LocaleResources } from "@multica/core/i18n";
import agents from "../locales-test/en/agents.json";
import auth from "../locales-test/en/auth.json";
import autopilots from "../locales-test/en/autopilots.json";
import billing from "../locales-test/en/billing.json";
import chat from "../locales-test/en/chat.json";
import common from "../locales-test/en/common.json";
import editor from "../locales-test/en/editor.json";
import inbox from "../locales-test/en/inbox.json";
import invite from "../locales-test/en/invite.json";
import issues from "../locales-test/en/issues.json";
import labels from "../locales-test/en/labels.json";
import layout from "../locales-test/en/layout.json";
import life from "../locales-test/en/life.json";
import members from "../locales-test/en/members.json";
import modals from "../locales-test/en/modals.json";
import myIssues from "../locales-test/en/my-issues.json";
import onboarding from "../locales-test/en/onboarding.json";
import projects from "../locales-test/en/projects.json";
import runReviews from "../locales-test/en/run-reviews.json";
import runtimes from "../locales-test/en/runtimes.json";
import search from "../locales-test/en/search.json";
import settings from "../locales-test/en/settings.json";
import skills from "../locales-test/en/skills.json";
import squads from "../locales-test/en/squads.json";
import ui from "../locales-test/en/ui.json";
import usage from "../locales-test/en/usage.json";
import workspace from "../locales-test/en/workspace.json";

export const TEST_EN_RESOURCES: Record<"en", LocaleResources> = {
  en: {
    agents,
    auth,
    autopilots,
    billing,
    chat,
    common,
    editor,
    inbox,
    invite,
    issues,
    labels,
    layout,
    life,
    members,
    modals,
    "my-issues": myIssues,
    onboarding,
    projects,
    "run-reviews": runReviews,
    runtimes,
    search,
    settings,
    skills,
    squads,
    ui,
    usage,
    workspace,
  },
};
