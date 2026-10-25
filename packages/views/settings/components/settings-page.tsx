// 本文件是设置页顶层组件：渲染左侧标签导航和右侧对应标签内容，并按 feature flag 控制部分标签是否可见。

"use client";

import { useQuery } from "@tanstack/react-query";
import { testAgentControlCapabilityOptions } from "@multica/core/test-agents";
import React, { lazy, Suspense } from "react";
import {
  User,
  SlidersHorizontal,
  Key,
  Building2,
  Users,
  FolderGit2,
  Bell,
  Plug,
  MessageCircle,
  Tags,
  CircleDot,
  Keyboard,
  ListTodo,
  Zap,
  Blocks,
  Server,
} from "lucide-react";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@multica/ui/components/ui/tabs";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useWorkspacePaths, useCurrentWorkspace } from "@multica/core/paths";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { useFeatureEnabled, useConfigStore } from "@multica/core/config";
import { useNavigation } from "../../navigation";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { CollapsedNavTrigger } from "../../layout/page-header";
import { useT } from "../../i18n";

const TestAgentsTab = lazy(() => import("./test-agents-tab").then(module => ({default:module.TestAgentsTab})));
const AccountTab = lazy(() => import("./account-tab").then((module) => ({ default: module.AccountTab })));
const PreferencesTab = lazy(() => import("./preferences-tab").then((module) => ({ default: module.PreferencesTab })));
const ChatTab = lazy(() => import("./chat-tab").then((module) => ({ default: module.ChatTab })));
const IssueTab = lazy(() => import("./issue-tab").then((module) => ({ default: module.IssueTab })));
const TokensTab = lazy(() => import("./tokens-tab").then((module) => ({ default: module.TokensTab })));
const WorkspaceTab = lazy(() => import("./workspace-tab").then((module) => ({ default: module.WorkspaceTab })));
const MembersTab = lazy(() => import("./members-tab").then((module) => ({ default: module.MembersTab })));
const RepositoriesTab = lazy(() => import("./repositories-tab").then((module) => ({ default: module.RepositoriesTab })));
const IntegrationsTab = lazy(() => import("./integrations-tab").then((module) => ({ default: module.IntegrationsTab })));
const NotificationsTab = lazy(() => import("./notifications-tab").then((module) => ({ default: module.NotificationsTab })));
const LabelsTab = lazy(() => import("./labels-tab").then((module) => ({ default: module.LabelsTab })));
const IssueStatusesTab = lazy(() => import("./issue-statuses-tab").then((module) => ({ default: module.IssueStatusesTab })));
const PropertiesTab = lazy(() => import("./properties-tab").then((module) => ({ default: module.PropertiesTab })));
const QuickActionsTab = lazy(() => import("./quick-actions-tab").then((module) => ({ default: module.QuickActionsTab })));
const KeyboardShortcutsTab = lazy(() => import("./keyboard-shortcuts-tab").then((module) => ({ default: module.KeyboardShortcutsTab })));
const PluginsTab = lazy(() => import("./plugins-tab").then((module) => ({ default: module.PluginsTab })));
const McpTab = lazy(() => import("./mcp-tab").then((module) => ({ default: module.McpTab })));

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
    case "test-agents": return <TestAgentsTab />;
    case "workspace": return <WorkspaceTab />;
    case "repositories": return <RepositoriesTab />;
    case "github": return <RepositoriesTab />;
    case "integrations": return <IntegrationsTab />;
    case "members": return <MembersTab />;
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
  "test_agents",
  "general",
  "repositories",
  "integrations",
  "members",
  "labels",
  "issue_statuses",
  "properties",
  "plugins",
] as const;
const WORKSPACE_TAB_VALUES = {
  test_agents: "test-agents",
  general: "workspace",
  repositories: "repositories",
  integrations: "integrations",
  members: "members",
  labels: "labels",
  issue_statuses: "issue-statuses",
  properties: "properties",
  quick_actions: "quick-actions",
  mcp: "mcp",
  plugins: "plugins",
} as const;
const WORKSPACE_TAB_ICONS = {
  test_agents: Server,
  general: Building2,
  repositories: FolderGit2,
  integrations: Plug,
  members: Users,
  labels: Tags,
  issue_statuses: CircleDot,
  properties: SlidersHorizontal,
  quick_actions: Zap,
  mcp: Server,
  plugins: Blocks,
} as const;

const DEFAULT_TAB = "profile";
const TAB_QUERY_KEY = "tab";

// 历史遗留的 `?tab=…` 取值，已被合并进其它标签。老书签仍能落到正确的页面上，
// 而无需保留一个已废弃的 TabsContent 项。Lark 曾经是独立的一级工作区标签，
// 现在并入「集成」里了。
const LEGACY_WORKSPACE_TAB_REDIRECTS: Record<string, string> = {
  lark: "integrations",
  github: "repositories",
};

const SETTINGS_TAB_TRIGGER_CLASS =
  "h-8 shrink-0 px-2.5 hover:bg-surface-hover data-active:!bg-surface-selected data-active:!text-surface-selected-foreground data-active:hover:!bg-surface-selected md:!w-full md:px-2 md:after:hidden";

/**
 * 设置页顶层组件。左侧是「我的账户」和「工作区」两组标签导航，右侧渲染当前标签内容；
 * plugins 标签是否显示由 feature flag 决定。
 */
export function SettingsPage() {
  const { t } = useT("settings");
  const paths = useWorkspacePaths();
  const workspace = useCurrentWorkspace();
  const workspaceName = workspace?.name;
  const navigation = useNavigation();
  const isMobile = useIsMobile();
  const production = useConfigStore(state => state.environmentName) === "生产";
  const ws = workspace?.id ?? "";
  const capability = useQuery({
    ...testAgentControlCapabilityOptions(ws),
    enabled: production && !!ws,
    retry: false,
  });
  const testAgentsAvailable = production && capability.data === true;
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);

  // 只保留被 feature flag 打开的标签；plugins 默认隐藏。
  const visibleWorkspaceTabKeys = React.useMemo(
    () =>
      WORKSPACE_TAB_KEYS.filter(
        (key) =>
          (key !== "plugins" || pluginsEnabled) && (key !== "test_agents" || testAgentsAvailable),
      ),
    [pluginsEnabled, testAgentsAvailable],
  );

  // 有效标签值的白名单；未知的 ?tab=… 值会静默回退到默认标签。
  // 白名单同时也能阻止 ?tab=<script> 之类的垃圾值通过 Radix Tabs 内部泄漏到 DOM。
  const validTabs = React.useMemo(
    () =>
      new Set<string>([
        ...ACCOUNT_TAB_KEYS,
        ...visibleWorkspaceTabKeys.map((key) => WORKSPACE_TAB_VALUES[key]),
      ]),
    [visibleWorkspaceTabKeys],
  );

  const tabFromUrl = navigation.searchParams.get(TAB_QUERY_KEY);
  React.useEffect(() => {
    if (tabFromUrl !== "mcp" && tabFromUrl !== "quick-actions" && tabFromUrl !== "quick_actions") return;
    const params = new URLSearchParams(navigation.searchParams);
    params.delete(TAB_QUERY_KEY);
    const target = tabFromUrl === "mcp" ? paths.mcp() : paths.quickActions();
    navigation.replace(params.size ? `${target}?${params}` : target);
  }, [tabFromUrl, paths, navigation]);
  const candidateTab = tabFromUrl
    ? LEGACY_WORKSPACE_TAB_REDIRECTS[tabFromUrl] ?? tabFromUrl
    : null;
  const activeTab =
    candidateTab && validTabs.has(candidateTab) ? candidateTab : DEFAULT_TAB;

  // 用 replace（而非 push）切换，避免设置页的标签切换污染浏览器历史记录。
  // 同时保留页面上可能携带的其它查询参数。
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
      {/* 结构导航；带边界的设置分组仍留在内容区里。
          保持内容区背景色（不加外壳色）：桌面端活动标签会与卡片顶部融合，
          若在首个标签下加一块着色面板会破坏这个接缝（MUL-4439）。区域划分改由分割线承担。 */}
      <div className="shrink-0 overflow-x-auto border-b border-surface-border p-2 md:w-56 md:overflow-y-auto md:border-b-0 md:border-r md:p-4">
        {/* 本页自建了页面框架而不是用 PageHeader，所以必须自己提供导航触发器——
            在 `xl` 以下导航是抽屉或自动折叠的，设置页除此之外没有返回导航的途径。 */}
        {/* 这一行下方的间距属于行本身，而不属于标题：配合 `items-center` 时，
            给 `h1` 加的下边距会成为被居中盒的一部分，从而使标题相对旁边的触发器产生偏移。 */}
        <div className="flex items-center md:mb-4">
          <CollapsedNavTrigger />
          <h1 className="sr-only text-body font-semibold md:not-sr-only md:px-2">{t(($) => $.page.title)}</h1>
        </div>
        <TabsList
          variant="line"
          className="flex w-max min-w-full flex-row items-center gap-1 p-0 md:w-full md:flex-col md:items-stretch"
        >
          {/* 我的账户分组 */}
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
          {/* 工作区分组 */}
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

      {/* 右侧内容区 */}
      <div className="min-w-0 flex-1 md:overflow-y-auto">
        <div className={`mx-auto w-full p-4 sm:p-6 md:p-8 ${activeTab === "repositories" || activeTab === "labels" || activeTab === "issue-statuses" || activeTab === "properties" || activeTab === "quick-actions"
              ? activeTab === "repositories" ? "max-w-[1340px]" : "max-w-5xl"
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
