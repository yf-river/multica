"use client";

import React, { lazy, Suspense } from "react";
import {
  User,
  SlidersHorizontal,
  Key,
  Settings,
  Users,
  FolderGit2,
  FlaskConical,
  Bell,
  Plug,
  MessageCircle,
  Tags,
  CircleDot,
  Keyboard,
  ListTodo,
  Zap,
  Blocks,
  CreditCard,
  Server,
} from "lucide-react";
import { GitHubMark } from "./github-mark";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@multica/ui/components/ui/tabs";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useFeatureEnabled } from "@multica/core/config";
import {
  BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG,
  PLUGINS_V1_FLAG,
} from "@multica/core/feature-flags";
import { useNavigation } from "../../navigation";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { CollapsedNavTrigger } from "../../layout/page-header";
import { useT } from "../../i18n";

const AccountTab = lazy(() => import("./account-tab").then((module) => ({ default: module.AccountTab })));
const PreferencesTab = lazy(() => import("./preferences-tab").then((module) => ({ default: module.PreferencesTab })));
const ChatTab = lazy(() => import("./chat-tab").then((module) => ({ default: module.ChatTab })));
const IssueTab = lazy(() => import("./issue-tab").then((module) => ({ default: module.IssueTab })));
const TokensTab = lazy(() => import("./tokens-tab").then((module) => ({ default: module.TokensTab })));
const WorkspaceTab = lazy(() => import("./workspace-tab").then((module) => ({ default: module.WorkspaceTab })));
const MembersTab = lazy(() => import("./members-tab").then((module) => ({ default: module.MembersTab })));
const RepositoriesTab = lazy(() => import("./repositories-tab").then((module) => ({ default: module.RepositoriesTab })));
const GitHubTab = lazy(() => import("./github-tab").then((module) => ({ default: module.GitHubTab })));
const IntegrationsTab = lazy(() => import("./integrations-tab").then((module) => ({ default: module.IntegrationsTab })));
const LabsTab = lazy(() => import("./labs-tab").then((module) => ({ default: module.LabsTab })));
const NotificationsTab = lazy(() => import("./notifications-tab").then((module) => ({ default: module.NotificationsTab })));
const LabelsTab = lazy(() => import("./labels-tab").then((module) => ({ default: module.LabelsTab })));
const IssueStatusesTab = lazy(() => import("./issue-statuses-tab").then((module) => ({ default: module.IssueStatusesTab })));
const PropertiesTab = lazy(() => import("./properties-tab").then((module) => ({ default: module.PropertiesTab })));
const QuickActionsTab = lazy(() => import("./quick-actions-tab").then((module) => ({ default: module.QuickActionsTab })));
const KeyboardShortcutsTab = lazy(() => import("./keyboard-shortcuts-tab").then((module) => ({ default: module.KeyboardShortcutsTab })));
const PluginsTab = lazy(() => import("./plugins-tab").then((module) => ({ default: module.PluginsTab })));
const McpTab = lazy(() => import("./mcp-tab").then((module) => ({ default: module.McpTab })));
const BillingTab = lazy(() => import("./billing-tab").then((module) => ({ default: module.BillingTab })));

function SettingsTabFallback() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-7 w-40" />
      <Skeleton className="h-24 w-full" />
      <Skeleton className="h-24 w-full" />
    </div>
  );
}

function ActiveSettingsTab({ tab }: { tab: string }) {
  switch (tab) {
    case "preferences": return <PreferencesTab />;
    case "shortcuts": return <KeyboardShortcutsTab />;
    case "issue": return <IssueTab />;
    case "chat": return <ChatTab />;
    case "notifications": return <NotificationsTab />;
    case "tokens": return <TokensTab />;
    case "workspace": return <WorkspaceTab />;
    case "repositories": return <RepositoriesTab />;
    case "github": return <GitHubTab />;
    case "integrations": return <IntegrationsTab />;
    case "labs": return <LabsTab />;
    case "members": return <MembersTab />;
    case "billing": return <BillingTab />;
    case "labels": return <LabelsTab />;
    case "issue-statuses": return <IssueStatusesTab />;
    case "properties": return <PropertiesTab />;
    case "quick-actions": return <QuickActionsTab />;
    case "mcp": return <McpTab />;
    case "plugins": return <PluginsTab />;
    default: return <AccountTab />;
  }
}

const ACCOUNT_TAB_KEYS = ["profile", "preferences", "shortcuts", "issue", "chat", "notifications", "tokens"] as const;
const ACCOUNT_TAB_ICONS = {
  profile: User,
  preferences: SlidersHorizontal,
  shortcuts: Keyboard,
  issue: ListTodo,
  chat: MessageCircle,
  notifications: Bell,
  tokens: Key,
} as const;

const WORKSPACE_TAB_KEYS = [
  "general",
  "repositories",
  "github",
  "integrations",
  "labs",
  "members",
  "billing",
  "labels",
  "issue_statuses",
  "properties",
  "quick_actions",
  "mcp",
  "plugins",
] as const;
const WORKSPACE_TAB_VALUES = {
  general: "workspace",
  repositories: "repositories",
  github: "github",
  integrations: "integrations",
  labs: "labs",
  members: "members",
  billing: "billing",
  labels: "labels",
  issue_statuses: "issue-statuses",
  properties: "properties",
  quick_actions: "quick-actions",
  mcp: "mcp",
  plugins: "plugins",
} as const;
const WORKSPACE_TAB_ICONS = {
  general: Settings,
  repositories: FolderGit2,
  github: GitHubMark,
  integrations: Plug,
  labs: FlaskConical,
  members: Users,
  billing: CreditCard,
  labels: Tags,
  issue_statuses: CircleDot,
  properties: SlidersHorizontal,
  quick_actions: Zap,
  mcp: Server,
  plugins: Blocks,
} as const;

const DEFAULT_TAB = "profile";
const TAB_QUERY_KEY = "tab";

// Legacy `?tab=…` values that have been collapsed into another tab. Old
// bookmarks still land on the correct surface without us preserving a
// dead TabsContent entry. Lark used to be its own top-level workspace
// tab; it now lives inside Integrations.
const LEGACY_WORKSPACE_TAB_REDIRECTS: Record<string, string> = {
  lark: "integrations",
};

const SETTINGS_TAB_TRIGGER_CLASS =
  "h-8 shrink-0 px-2.5 hover:bg-surface-hover data-active:!bg-surface-selected data-active:!text-surface-selected-foreground data-active:hover:!bg-surface-selected md:!w-full md:px-2 md:after:hidden";

export function SettingsPage() {
  const { t } = useT("settings");
  const workspaceName = useCurrentWorkspace()?.name;
  const navigation = useNavigation();
  const isMobile = useIsMobile();
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);
  const billingEnabled = useFeatureEnabled(
    BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG,
    false,
  );

  const visibleWorkspaceTabKeys = React.useMemo(
    () =>
      WORKSPACE_TAB_KEYS.filter(
        (key) =>
          (key !== "plugins" || pluginsEnabled) &&
          (key !== "billing" || billingEnabled),
      ),
    [billingEnabled, pluginsEnabled],
  );

  // Whitelist of valid tab values; unknown ?tab=… values silently fall back to
  // the default. Whitelisting also blocks junk like ?tab=<script> from
  // surfacing in the DOM via Radix Tabs internals.
  const validTabs = React.useMemo(
    () =>
      new Set<string>([
        ...ACCOUNT_TAB_KEYS,
        ...visibleWorkspaceTabKeys.map((key) => WORKSPACE_TAB_VALUES[key]),
      ]),
    [visibleWorkspaceTabKeys],
  );

  const tabFromUrl = navigation.searchParams.get(TAB_QUERY_KEY);
  const candidateTab = tabFromUrl
    ? tabFromUrl === "billing" && !billingEnabled
      ? "workspace"
      : LEGACY_WORKSPACE_TAB_REDIRECTS[tabFromUrl] ?? tabFromUrl
    : null;
  const activeTab =
    candidateTab && validTabs.has(candidateTab) ? candidateTab : DEFAULT_TAB;

  // replace (not push) so settings tab switches don't pollute browser history.
  // Preserve any other query params the page may carry.
  const handleTabChange = (next: string) => {
    const params = new URLSearchParams(navigation.searchParams);
    params.set(TAB_QUERY_KEY, next);
    navigation.replace(`${navigation.pathname}?${params.toString()}`);
  };

  return (
    <Tabs
      value={activeTab}
      onValueChange={handleTabChange}
      orientation={isMobile ? "horizontal" : "vertical"}
      className="flex flex-1 min-h-0 flex-col gap-0 overflow-y-auto md:flex-row md:overflow-hidden"
    >
      {/* Structural navigation; bounded setting groups remain in the content surface.
          Stays on the content surface color (no shell tint): the desktop's active
          tab merges into the card top, and a tinted panel under the first tabs
          breaks that seam (MUL-4439). Zoning comes from the divider instead. */}
      <div className="shrink-0 overflow-x-auto border-b border-surface-border p-2 md:w-56 md:overflow-y-auto md:border-b-0 md:border-r md:p-4">
        {/* This page builds its own chrome instead of a PageHeader, so it has
            to supply the nav trigger itself — below `xl` the nav is a sheet or
            auto-collapsed, and settings has no other way back to it. */}
        {/* The gap below this row belongs to the row, not to the heading: with
            `items-center`, a bottom margin on the `h1` is part of the box being
            centred, so it offsets the heading against the trigger beside it. */}
        <div className="flex items-center md:mb-4">
          <CollapsedNavTrigger />
          <h1 className="sr-only text-body font-semibold md:not-sr-only md:px-2">{t(($) => $.page.title)}</h1>
        </div>
        <TabsList
          variant="line"
          className="flex w-max min-w-full flex-row items-center gap-1 p-0 md:w-full md:flex-col md:items-stretch"
        >
          {/* My Account group */}
          <span className="hidden px-2 pb-1 pt-2 text-caption font-medium text-muted-foreground md:block">
            {t(($) => $.page.my_account)}
          </span>
          {ACCOUNT_TAB_KEYS.map((key) => {
            const Icon = ACCOUNT_TAB_ICONS[key];
            return (
              <TabsTrigger
                key={key}
                value={key}
                className={SETTINGS_TAB_TRIGGER_CLASS}
              >
                <Icon className="h-4 w-4" />
                {t(($) => $.page.tabs[key])}
              </TabsTrigger>
            );
          })}
          {/* Workspace group */}
          <span className="hidden truncate px-2 pb-1 pt-4 text-caption font-medium text-muted-foreground md:block">
            {workspaceName ?? t(($) => $.page.workspace_fallback)}
          </span>
          {visibleWorkspaceTabKeys.map((key) => {
            const Icon = WORKSPACE_TAB_ICONS[key];
            return (
              <TabsTrigger
                key={key}
                value={WORKSPACE_TAB_VALUES[key]}
                className={SETTINGS_TAB_TRIGGER_CLASS}
              >
                <Icon className="h-4 w-4" />
                {t(($) => $.page.tabs[key])}
              </TabsTrigger>
            );
          })}
        </TabsList>
      </div>

      {/* Right content */}
      <div className="min-w-0 flex-1 md:overflow-y-auto">
        <div className={`mx-auto w-full p-4 sm:p-6 md:p-8 ${activeTab === "labels" || activeTab === "issue-statuses" || activeTab === "properties" || activeTab === "quick-actions"
              ? "max-w-5xl"
              : "max-w-3xl"}`}>
          <Suspense fallback={<SettingsTabFallback />}>
            <TabsContent value={activeTab}>
              <ActiveSettingsTab tab={activeTab} />
            </TabsContent>
          </Suspense>
        </div>
      </div>
    </Tabs>
  );
}
