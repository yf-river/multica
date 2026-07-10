import "i18next";
// Pulls in the `ui` namespace augmentation owned by packages/ui — see
// packages/ui/types/i18next.ts. Side-effect import is required for views'
// typecheck program to see ui's contribution to `I18nResources`.
import "@multica/ui/i18n-types";
import type common from "../locales/zh-Hans/common.json";
import type auth from "../locales/zh-Hans/auth.json";
import type settings from "../locales/zh-Hans/settings.json";
import type issues from "../locales/zh-Hans/issues.json";
import type agents from "../locales/zh-Hans/agents.json";
import type editor from "../locales/zh-Hans/editor.json";
import type labels from "../locales/zh-Hans/labels.json";
import type members from "../locales/zh-Hans/members.json";
import type myIssues from "../locales/zh-Hans/my-issues.json";
import type search from "../locales/zh-Hans/search.json";
import type inbox from "../locales/zh-Hans/inbox.json";
import type workspace from "../locales/zh-Hans/workspace.json";
import type projects from "../locales/zh-Hans/projects.json";
import type autopilots from "../locales/zh-Hans/autopilots.json";
import type skills from "../locales/zh-Hans/skills.json";
import type chat from "../locales/zh-Hans/chat.json";
import type modals from "../locales/zh-Hans/modals.json";
import type runtimes from "../locales/zh-Hans/runtimes.json";
import type layout from "../locales/zh-Hans/layout.json";
import type usage from "../locales/zh-Hans/usage.json";
import type squads from "../locales/zh-Hans/squads.json";
import type agentPlayground from "../locales/zh-Hans/agent-playground.json";
import type runReviews from "../locales/zh-Hans/run-reviews.json";
import type promptLibrary from "../locales/zh-Hans/prompt-library.json";

// Module augmentation enables i18next v26 selector API across the monorepo:
// `t($ => $.signin.title)` resolves to the value in zh-Hans/auth.json.
// Apps don't need to redeclare this — the augmentation is global, pulled
// into the compilation graph by `use-t.ts`'s side-effect import.
//
// Adding a namespace: drop a JSON file under the zh-Hans locale directory, then add
// the matching `import type` + entry below. Type inference on `t($ => $)`
// follows automatically.
//
// The resource shape lives on a global `I18nResources` interface (not a
// type literal inside CustomTypeOptions) so other packages can contribute
// namespaces via declaration merging. See packages/ui/types/i18next.d.ts —
// it adds the `ui` namespace there, which lets packages/ui typecheck the
// selector form standalone without depending on @multica/views.
declare global {
  interface I18nResources {
    common: typeof common;
    auth: typeof auth;
    settings: typeof settings;
    issues: typeof issues;
    agents: typeof agents;
    editor: typeof editor;
    labels: typeof labels;
    members: typeof members;
    "my-issues": typeof myIssues;
    search: typeof search;
    inbox: typeof inbox;
    workspace: typeof workspace;
    projects: typeof projects;
    autopilots: typeof autopilots;
    skills: typeof skills;
    chat: typeof chat;
    modals: typeof modals;
    runtimes: typeof runtimes;
    layout: typeof layout;
    usage: typeof usage;
    squads: typeof squads;
    "agent-playground": typeof agentPlayground;
    "run-reviews": typeof runReviews;
    "prompt-library": typeof promptLibrary;
  }
}

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "common";
    resources: I18nResources;
    enableSelector: true;
  }
}
