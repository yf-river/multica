import type { LocaleResources } from "@multica/core/i18n";
import agents from "./zh-Hans/agents.json";
import auth from "./zh-Hans/auth.json";
import autopilots from "./zh-Hans/autopilots.json";
import billing from "./zh-Hans/billing.json";
import chat from "./zh-Hans/chat.json";
import common from "./zh-Hans/common.json";
import editor from "./zh-Hans/editor.json";
import inbox from "./zh-Hans/inbox.json";
import invite from "./zh-Hans/invite.json";
import issues from "./zh-Hans/issues.json";
import labels from "./zh-Hans/labels.json";
import layout from "./zh-Hans/layout.json";
import life from "./zh-Hans/life.json";
import members from "./zh-Hans/members.json";
import modals from "./zh-Hans/modals.json";
import myIssues from "./zh-Hans/my-issues.json";
import onboarding from "./zh-Hans/onboarding.json";
import projects from "./zh-Hans/projects.json";
import runReviews from "./zh-Hans/run-reviews.json";
import runtimes from "./zh-Hans/runtimes.json";
import search from "./zh-Hans/search.json";
import settings from "./zh-Hans/settings.json";
import skills from "./zh-Hans/skills.json";
import squads from "./zh-Hans/squads.json";
import ui from "./zh-Hans/ui.json";
import usage from "./zh-Hans/usage.json";
import workspace from "./zh-Hans/workspace.json";

export const RESOURCES: Record<"zh-Hans", LocaleResources> = {
  "zh-Hans": {
    agents: agents,
    auth: auth,
    autopilots: autopilots,
    billing: billing,
    chat: chat,
    common: common,
    editor: editor,
    inbox: inbox,
    invite: invite,
    issues: issues,
    labels: labels,
    layout: layout,
    life: life,
    members: members,
    modals: modals,
    "my-issues": myIssues,
    onboarding: onboarding,
    projects: projects,
    "run-reviews": runReviews,
    runtimes: runtimes,
    search: search,
    settings: settings,
    skills: skills,
    squads: squads,
    ui: ui,
    usage: usage,
    workspace: workspace,
  },
};
