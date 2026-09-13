// 本文件是「GitHub 集成」设置标签页（设置页一级标签）：总开关控制是否启用 GitHub
// 能力；连接分区管理 GitHub App 安装（连接 / 断开）；功能分区提供 PR 侧边栏、
// 提交共同作者署名、PR 自动关联开关；仓库分区是前往「仓库设置」的快捷入口。

"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ExternalLink, GitCommitHorizontal, Link2, PanelRight } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import {
  deriveGitHubSettings,
  githubInstallationsOptions,
} from "@multica/core/github";
import { api } from "@multica/core/api";
import type { Workspace } from "@multica/core/types";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";
import { GitHubMark } from "./github-mark";

/** 本页能改写的几个工作区设置键名。 */
type SettingsKey =
  | "github_enabled"
  | "github_pr_sidebar_enabled"
  | "co_authored_by_enabled"
  | "github_auto_link_prs_enabled";

/**
 * GitHub 集成设置标签页。自上而下四块：
 * - 主开关：是否启用 GitHub 能力（连接/功能开关的父开关）；
 * - 连接：展示已连接账号，可发起连接 / 断开 GitHub App 安装；
 * - 功能：PR 侧边栏、Co-authored-by 署名、PR 自动关联三个子开关（主开关关闭时禁用）；
 * - 仓库：跳转到「仓库设置」的快捷入口。
 * 只有管理员（can_manage，来自后端响应）能改这些设置；其余成员只读。
 */
export function GitHubTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const navigation = useNavigation();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  // `canView` 只负责放行只读的安装列表（MUL-2413 后每个工作区成员都能看到）；
  // `canManage` 才决定「连接/断开」权限，取值来自后端返回的 `can_manage`——
  // 前端绝不自行宣称那些服务端会拒绝的管理权限。
  const canView = !!currentMember;

  const { data: installationData } = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: !!wsId && canView,
  });
  const installations = installationData?.installations ?? [];
  const configured = installationData?.configured ?? false;
  const canManage = installationData?.can_manage === true;
  const connected = installations.length > 0;
  const primaryInstallation = installations[0] ?? null;

  const flags = deriveGitHubSettings(workspace);
  const [savingKey, setSavingKey] = useState<SettingsKey | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  // 保存某个设置开关：合并进工作区已有 settings 后整体提交，并把返回值写回
  // React Query 里的工作区列表缓存（避免下次读取还是旧值）。
  async function persistSetting(key: SettingsKey, next: boolean) {
    if (!workspace || savingKey) return;
    setSavingKey(key);
    try {
      const merged = {
        ...((workspace.settings as Record<string, unknown>) ?? {}),
        [key]: next,
      };
      const updated = await api.updateWorkspace(workspace.id, { settings: merged });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_failed));
    } finally {
      setSavingKey(null);
    }
  }

  async function handleConnect() {
    setConnecting(true);
    try {
      const resp = await api.getGitHubConnectURL(wsId);
      if (!resp.configured || !resp.url) {
        toast.error(t(($) => $.github.toast_not_configured));
        return;
      }
      window.open(resp.url, "_blank", "noopener");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_open_failed));
    } finally {
      setConnecting(false);
    }
  }

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteGitHubInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: ["github", wsId] });
      toast.success(t(($) => $.github.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  if (!workspace) return null;

  const repositoriesHref = `${navigation.pathname}?tab=repositories`;

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.github)}
      description={t(($) => $.github.page_description)}
    >
      <section className="space-y-3">
        <Card>
          <CardContent>
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">
                  <GitHubMark className="h-4 w-4" />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="github-master" className="text-body font-medium">
                    {t(($) => $.github.section_master)}
                  </Label>
                  <p className="text-body text-muted-foreground">
                    {flags.enabled
                      ? t(($) => $.github.master_description_on)
                      : t(($) => $.github.master_description_off)}
                  </p>
                </div>
              </div>
              <Switch
                id="github-master"
                checked={flags.enabled}
                onCheckedChange={(v) => persistSetting("github_enabled", v)}
                disabled={!canManage || savingKey === "github_enabled"}
              />
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_connection)}</h2>
        <Card>
          <CardContent className="space-y-4">
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <GitHubMark className="h-6 w-6 mt-0.5 shrink-0" />
                <div className="space-y-1">
                  <p className="text-body font-medium">{t(($) => $.github.connection_title)}</p>
                  {connected ? (
                    <>
                      <p className="text-caption text-muted-foreground">
                        {t(($) => $.github.connected_to, {
                          login: installations.map((i) => i.account_login).join(", "),
                        })}
                      </p>
                      {primaryInstallation?.connected_by && (
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.github.connected_by, {
                            name: primaryInstallation.connected_by!,
                          })}
                        </p>
                      )}
                    </>
                  ) : canManage ? (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.github.connection_description_prefix)}{" "}
                      <code className="rounded bg-muted px-1 py-0.5 text-micro">
                        {t(($) => $.github.connection_identifier_example)}
                      </code>{" "}
                      {t(($) => $.github.connection_description_suffix)}{" "}
                      <strong>{t(($) => $.github.connection_description_done)}</strong>.
                    </p>
                  ) : (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.github.contact_admin_to_connect)}
                    </p>
                  )}
                </div>
              </div>
              {canManage && (
                <div className="flex items-center gap-2">
                  {connected && primaryInstallation ? (
                    // 断开入口在主开关关闭时也必须保留——「断开」是撤销 GitHub App
                    // 授权，与「隐藏该功能」（主开关）是两码事，不能互相牵连。
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setDisconnectTarget(primaryInstallation.id)}
                    >
                      {t(($) => $.github.disconnect)}
                    </Button>
                  ) : (
                    <Button
                      size="sm"
                      onClick={handleConnect}
                      disabled={connecting || !configured}
                      title={
                        !configured
                          ? t(($) => $.github.connect_disabled_tooltip)
                          : undefined
                      }
                    >
                      {connecting
                        ? t(($) => $.github.connect_opening)
                        : t(($) => $.github.connect_github)}
                    </Button>
                  )}
                </div>
              )}
            </div>

            {canManage && !configured && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.not_configured)}{" "}
                <code className="rounded bg-muted px-1 py-0.5 text-micro">GITHUB_APP_SLUG</code>{" "}
                {t(($) => $.github.not_configured_and)}{" "}
                <code className="rounded bg-muted px-1 py-0.5 text-micro">GITHUB_WEBHOOK_SECRET</code>.
              </p>
            )}

            {!canManage && connected && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.read_only_hint)}
              </p>
            )}
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_features)}</h2>
        <Card className="gap-0 py-0">
          <CardContent className="divide-y divide-surface-border px-0">
            <FeatureRow
              id="github-pr-sidebar"
              icon={<PanelRight className="h-4 w-4" />}
              label={t(($) => $.github.feature_pr_sidebar_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_pr_sidebar_description)}
                </p>
              }
              checked={flags.prSidebar}
              disabled={!canManage || !flags.enabled || savingKey === "github_pr_sidebar_enabled"}
              onCheckedChange={(v) => persistSetting("github_pr_sidebar_enabled", v)}
            />

            <FeatureRow
              id="github-coauthor"
              icon={<GitCommitHorizontal className="h-4 w-4" />}
              label={t(($) => $.github.feature_co_author_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_co_author_description_prefix)}{" "}
                  <code className="rounded bg-muted px-1 py-0.5 text-caption">
                    {"Co-authored-by: multica-agent <github@multica.ai>"}
                  </code>{" "}
                  {t(($) => $.github.feature_co_author_description_suffix)}
                </p>
              }
              checked={flags.coAuthor}
              disabled={!canManage || !flags.enabled || savingKey === "co_authored_by_enabled"}
              onCheckedChange={(v) => persistSetting("co_authored_by_enabled", v)}
            />

            <FeatureRow
              id="github-auto-link"
              icon={<Link2 className="h-4 w-4" />}
              label={t(($) => $.github.feature_auto_link_label)}
              description={
                <p className="text-body text-muted-foreground">
                  {t(($) => $.github.feature_auto_link_description)}
                </p>
              }
              checked={flags.autoLinkPRs}
              disabled={!canManage || !flags.enabled || savingKey === "github_auto_link_prs_enabled"}
              onCheckedChange={(v) => persistSetting("github_auto_link_prs_enabled", v)}
            />
          </CardContent>
        </Card>
      </section>

      <section className="space-y-3">
        <h2 className="text-body font-semibold">{t(($) => $.github.section_repositories)}</h2>
        <Card>
          <CardContent>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-body font-medium">
                {t(($) => $.github.repositories_shortcut_label)}
              </p>
              <Button
                variant="outline"
                size="sm"
                render={<AppLink href={repositoriesHref} />}
                nativeButton={false}
              >
                <ExternalLink className="h-3 w-3" />
                {t(($) => $.github.repositories_shortcut_link)}
              </Button>
            </div>
          </CardContent>
        </Card>
      </section>

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.github.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.github.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.github.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.github.disconnecting)
                : t(($) => $.github.disconnect_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

/**
 * 一个功能开关行：左边图标 + 标题 + 说明，右边是开关本体。disabled 时开关灰掉，
 * 常见于主开关未开、非管理员或正在保存该键。
 */
function FeatureRow({
  id,
  icon,
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string;
  icon: React.ReactNode;
  label: string;
  description: React.ReactNode;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-3.5">
      <div className="flex items-start gap-3">
        <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">{icon}</div>
        <div className="space-y-1">
          <Label htmlFor={id} className="text-body font-medium">
            {label}
          </Label>
          {description}
        </div>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={onCheckedChange}
      />
    </div>
  );
}
