// 本文件是「插件」设置标签页：管理员上传 .zip 发布插件包、评审并安装/升级指定版本，
// 管理已安装插件的启停、授权 scope、定时任务与 MCP 工具审批；普通成员只读查看。

"use client";

import { useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, CalendarClock, Loader2, Trash2, Upload } from "lucide-react";
import { toast } from "sonner";
import { useCurrentMember } from "@multica/core/permissions";
import {
  pluginInstallationsOptions,
  pluginPackagesOptions,
  useConfigurePlugin,
  useDeletePluginPackage,
  useInstallPlugin,
  usePreviewPlugin,
  usePublishPluginPackage,
  useSetPluginEnabled,
  useUninstallPlugin,
} from "@multica/core/plugins";
import { useCurrentWorkspace } from "@multica/core/paths";
import type {
  PluginConfigField,
  PluginInstallation,
  PluginPackage,
  PluginPreview,
} from "@multica/core/types";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Switch } from "@multica/ui/components/ui/switch";
import { mcpHooks, PluginHookActivity, PluginMCPApproval, PluginScheduleActivity } from "../../plugins";
import { useLocale, useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

/**
 * 权限（scope）列表就是整套信任模型：没有签名、没有发布者校验、也没有信任分层。
 * 所以授权确认页把原始的 scope 字符串逐个亮出来，旁边配一句大白话解释，绝不把它们
 * 概括掉——安装者得清楚插件到底要了什么权限。
 */
function ScopeList({ scopes, highlighted }: { scopes: string[]; highlighted?: string[] }) {
  const { t } = useT("settings");
  const added = new Set(highlighted ?? []);
  return (
    <ul className="space-y-1.5">
      {scopes.map((scope) => (
        <li key={scope} className="flex items-baseline gap-2 text-caption">
          <code className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono">{scope}</code>
          <span className="text-muted-foreground">{scopeDescription(scope, t)}</span>
          {added.has(scope) ? (
            <Badge variant="secondary">{t(($) => $.plugins.consent.new_scope)}</Badge>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/** 声明了定时任务（cron 表达式）的 hook 的最小展示结构。 */
type ScheduledHook = {
  key: string;
  name: string;
  schedule?: { cron: string; timezone: string; next_run_at?: string };
};

/** 定时任务列表：每个 hook 一行，展示中文频率描述、原始 cron 与时区，可选带"下次运行时间"。 */
function ScheduleList({ hooks, showNextRun = false }: { hooks: ScheduledHook[]; showNextRun?: boolean }) {
  const { t } = useT("settings");
  const locale = useLocale();
  return (
    <ul className="mt-2 space-y-1.5">
      {hooks.map((hook) => (
        <li key={hook.key} className="flex flex-wrap items-baseline gap-2 text-caption">
          <span className="font-medium">{hook.name}</span>
          <span>{scheduleFrequency(hook.schedule?.cron ?? "", t)}</span>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{hook.schedule?.cron ?? ""}</code>
          <span className="text-muted-foreground">{hook.schedule?.timezone ?? ""}</span>
          {showNextRun && hook.schedule?.next_run_at ? (
            <span className="text-muted-foreground">
              {t(($) => $.plugins.schedule.next_run, {
                time: formatScheduleTime(hook.schedule?.next_run_at, locale),
              })}
            </span>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/** 把 ISO 时间字符串按当前 locale 格式化成"日期 + 时间"，空值或非法值返回占位符 "—"。 */
function formatScheduleTime(value: string | undefined, locale: string): string {
  if (!value) return "—";
  const timestamp = Date.parse(value);
  if (Number.isNaN(timestamp)) return "—";
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(timestamp));
}

type Translate = ReturnType<typeof useT<"settings">>["t"];

/** 把标准 5 段 cron 表达式翻译成自然语言频率（每 N 分钟 / 每小时 / 每天固定时刻），无法识别时回退"自定义"。 */
function scheduleFrequency(cron: string, t: Translate): string {
  const fields = cron.trim().split(/\s+/);
  if (fields.length !== 5) return t(($) => $.plugins.schedule.frequency_custom);
  const everyMinutes = /^\*\/(\d+)$/.exec(fields[0] ?? "");
  if (everyMinutes && fields.slice(1).every((field) => field === "*")) {
    return t(($) => $.plugins.schedule.frequency_minutes, { count: Number(everyMinutes[1]) });
  }
  if (/^\d+$/.test(fields[0] ?? "") && fields.slice(1).every((field) => field === "*")) {
    return t(($) => $.plugins.schedule.frequency_hourly, { minute: fields[0] });
  }
  if (/^\d+$/.test(fields[0] ?? "") && /^\d+$/.test(fields[1] ?? "") && fields.slice(2).every((field) => field === "*")) {
    return t(($) => $.plugins.schedule.frequency_daily, {
      hour: String(fields[1]).padStart(2, "0"),
      minute: String(fields[0]).padStart(2, "0"),
    });
  }
  return t(($) => $.plugins.schedule.frequency_custom);
}

/** 把单个 scope 字符串翻译成一句人话（net: 前缀按域名解释，其余查表），未知的兜底为"未说明用途"。 */
function scopeDescription(scope: string, t: Translate): string {
  if (scope.startsWith("net:")) {
    return t(($) => $.plugins.scopes.net, { domain: scope.slice("net:".length) });
  }
  switch (scope) {
    case "issues:read": return t(($) => $.plugins.scopes.issues_read);
    case "issues:write": return t(($) => $.plugins.scopes.issues_write);
    case "comments:read": return t(($) => $.plugins.scopes.comments_read);
    case "comments:write": return t(($) => $.plugins.scopes.comments_write);
    case "tasks:read": return t(($) => $.plugins.scopes.tasks_read);
    case "tasks:write": return t(($) => $.plugins.scopes.tasks_write);
    case "agents:read": return t(($) => $.plugins.scopes.agents_read);
    case "members:read": return t(($) => $.plugins.scopes.members_read);
    case "storage:user": return t(($) => $.plugins.scopes.storage_user);
    case "storage:workspace": return t(($) => $.plugins.scopes.storage_workspace);
    default: return t(($) => $.plugins.scopes.unknown);
  }
}

/**
 * 配置表单是根据清单（manifest）字段生成的，而不是由插件提供：
 * 若允许渲染插件自己写的表单标记，等于把插件代码放到了我们自己的源站上。
 */
function ConfigForm({
  installation,
  canManage,
  wsId,
}: {
  installation: PluginInstallation;
  canManage: boolean;
  wsId: string;
}) {
  const { t } = useT("settings");
  const configureMutation = useConfigurePlugin(wsId);
  const [values, setValues] = useState<Record<string, unknown>>(() => ({ ...installation.config }));
  const [secrets, setSecrets] = useState<Record<string, string>>({});

  if (installation.config_schema.length === 0) return null;

  const setValue = (key: string, value: unknown) => setValues((current) => ({ ...current, [key]: value }));
  const configuredSecrets = new Set(installation.configured_secrets);

  const submit = async () => {
    const payload: Record<string, unknown> = { ...values };
    // 只提交管理员真实输入过的 secret。若把空串也发出去，每次保存无关字段时
    // 都会把已存的 secret 清空掉。
    for (const [key, value] of Object.entries(secrets)) {
      if (value.length > 0) payload[key] = value;
    }
    try {
      await configureMutation.mutateAsync({ installationId: installation.id, values: payload });
      setSecrets({});
      toast.success(t(($) => $.plugins.config.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.plugins.action_failed));
    }
  };

  return (
    <div className="space-y-4 border-t border-surface-border px-4 py-4">
      <div className="text-caption font-medium">{t(($) => $.plugins.config.title)}</div>
      {installation.config_schema.map((field) => (
        <ConfigField
          key={field.key}
          field={field}
          value={values[field.key]}
          secretValue={secrets[field.key] ?? ""}
          secretConfigured={configuredSecrets.has(field.key)}
          disabled={!canManage || configureMutation.isPending}
          onValueChange={(value) => setValue(field.key, value)}
          onSecretChange={(value) => setSecrets((current) => ({ ...current, [field.key]: value }))}
        />
      ))}
      <div className="flex justify-end">
        <Button size="sm" disabled={!canManage || configureMutation.isPending} onClick={submit}>
          {configureMutation.isPending ? <Loader2 className="animate-spin" /> : null}
          {t(($) => $.plugins.config.save)}
        </Button>
      </div>
    </div>
  );
}

function ConfigField({
  field,
  value,
  secretValue,
  secretConfigured,
  disabled,
  onValueChange,
  onSecretChange,
}: {
  field: PluginConfigField;
  value: unknown;
  secretValue: string;
  secretConfigured: boolean;
  disabled: boolean;
  onValueChange: (value: unknown) => void;
  onSecretChange: (value: string) => void;
}) {
  const { t } = useT("settings");
  const label = (
    <div className="min-w-0">
      <div className="text-caption font-medium">
        {field.label}
        {field.required ? <span className="ml-1 text-destructive">*</span> : null}
      </div>
      {field.description ? (
        <p className="mt-0.5 text-caption text-muted-foreground">{field.description}</p>
      ) : null}
    </div>
  );

  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
      {label}
      <div className="w-full sm:w-96">
        {field.type === "secret" ? (
          <Input
            type="password"
            autoComplete="off"
            disabled={disabled}
            value={secretValue}
            placeholder={secretConfigured
              ? t(($) => $.plugins.config.secret_set)
              : field.placeholder ?? ""}
            onChange={(event) => onSecretChange(event.target.value)}
          />
        ) : field.type === "bool" ? (
          <Switch
            disabled={disabled}
            checked={value === true}
            onCheckedChange={(checked) => onValueChange(checked === true)}
          />
        ) : field.type === "enum" ? (
          <Select
            items={(field.options ?? []).map((option) => ({ value: option, label: option }))}
            value={typeof value === "string" ? value : ""}
            onValueChange={(next) => next && onValueChange(next)}
          >
            <SelectTrigger disabled={disabled}><SelectValue /></SelectTrigger>
            <SelectContent>
              {(field.options ?? []).map((option) => (
                <SelectItem key={option} value={option}>{option}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : field.type === "number" ? (
          <Input
            type="number"
            disabled={disabled}
            value={typeof value === "number" ? String(value) : ""}
            placeholder={field.placeholder ?? ""}
            onChange={(event) => {
              const parsed = Number(event.target.value);
              onValueChange(event.target.value === "" || Number.isNaN(parsed) ? undefined : parsed);
            }}
          />
        ) : field.multiline === true ? (
          // 值是多行文本的字段，塞进单行输入框根本没法读——而且这份由清单生成的表单
          // 是宿主唯一自己掌控的插件 UI，做错了就是我们自己的锅，所以必须给多行输入框。
          <Textarea
            rows={4}
            disabled={disabled}
            value={typeof value === "string" ? value : ""}
            placeholder={field.placeholder ?? ""}
            onChange={(event) => onValueChange(event.target.value)}
          />
        ) : (
          <Input
            disabled={disabled}
            value={typeof value === "string" ? value : ""}
            placeholder={field.placeholder ?? ""}
            onChange={(event) => onValueChange(event.target.value)}
          />
        )}
      </div>
    </div>
  );
}

/**
 * 「发布插件」与「安装已发布的东西」。
 *
 * 它们放同一屏，因为本来就是一个故事的上下两半：作者上传产物，管理员安装其中某个
 * 具体版本。系统里不存在"没在这里发布过却能安装的代码"——这正是下面授权确认页的
 * 分量所在：它陈述的是对这段代码本身的信任，而不只是对清单的信任。
 */
function PublishAndInstall({ wsId, canManage }: { wsId: string; canManage: boolean }) {
  const { t } = useT("settings");
  const { data, isLoading } = useQuery(pluginPackagesOptions(wsId));
  const publishMutation = usePublishPluginPackage(wsId);
  const deleteMutation = useDeletePluginPackage(wsId);
  const previewMutation = usePreviewPlugin(wsId);
  const installMutation = useInstallPlugin(wsId);
  const fileRef = useRef<HTMLInputElement>(null);
  const [preview, setPreview] = useState<PluginPreview | null>(null);

  const packages = useMemo(() => data?.packages ?? [], [data]);
  const scheduledHooks = (preview?.manifest.contributes?.hooks ?? [])
    .filter((hook) => hook.schedule !== undefined);

  const reportError = (error: unknown) => {
    toast.error(error instanceof Error ? error.message : t(($) => $.plugins.action_failed));
  };

  const publish = async (file: File) => {
    try {
      const published = await publishMutation.mutateAsync(file);
      toast.success(t(($) => $.plugins.publish.published, {
        name: published.name,
        version: published.versions[0]?.version ?? "",
      }));
    } catch (error) {
      reportError(error);
    } finally {
      // 无条件清空文件框的值：这样修复被拒的包后再次选择同一个文件，也能重新触发
      // change 事件——而"再选一次同一个文件"正是开发者会做的动作。
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  const review = async (versionId: string) => {
    try {
      setPreview(await previewMutation.mutateAsync({ version_id: versionId }));
    } catch (error) {
      setPreview(null);
      reportError(error);
    }
  };

  const confirmInstall = async () => {
    if (!preview) return;
    try {
      await installMutation.mutateAsync({ version_id: preview.version_id, granted_scopes: preview.scopes });
      setPreview(null);
      toast.success(t(($) => $.plugins.consent.installed));
    } catch (error) {
      reportError(error);
    }
  };

  return (
    <SettingsSection title={t(($) => $.plugins.publish.title)} description={t(($) => $.plugins.publish.description)}>
      <SettingsCard>
        <div className="flex flex-col gap-2 px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-caption text-muted-foreground">{t(($) => $.plugins.publish.hint)}</p>
          <input
            ref={fileRef}
            type="file"
            accept=".zip,application/zip"
            className="sr-only"
            aria-label={t(($) => $.plugins.publish.upload)}
            onChange={(event) => {
              const file = event.target.files?.[0];
              if (file) void publish(file);
            }}
          />
          <Button
            disabled={!canManage || publishMutation.isPending}
            onClick={() => fileRef.current?.click()}
          >
            {publishMutation.isPending ? <Loader2 className="animate-spin" /> : <Upload />}
            {t(($) => $.plugins.publish.upload)}
          </Button>
        </div>

        {isLoading ? (
          <div className="border-t border-surface-border px-4 py-4">
            <Skeleton className="h-16 w-full" aria-label={t(($) => $.plugins.loading)} />
          </div>
        ) : packages.length === 0 ? (
          <p className="border-t border-surface-border px-4 py-6 text-caption text-muted-foreground">
            {t(($) => $.plugins.publish.empty)}
          </p>
        ) : (
          packages.map((pluginPackage) => (
            <PublishedPackage
              key={pluginPackage.id}
              pluginPackage={pluginPackage}
              canManage={canManage}
              busy={previewMutation.isPending || deleteMutation.isPending}
              onReview={review}
              onDelete={(packageId) => deleteMutation
                .mutateAsync(packageId)
                .then(() => toast.success(t(($) => $.plugins.publish.deleted)))
                .catch(reportError)}
            />
          ))
        )}

        {preview ? (
          <div className="space-y-4 border-t border-surface-border px-4 py-4">
            <div>
              <div className="text-body font-semibold">{preview.manifest.name}</div>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.plugins.byline, {
                  author: preview.manifest.author.name,
                  version: preview.version,
                })}
                {preview.installed
                  ? t(($) => $.plugins.consent.upgrade_from, { version: preview.installed_version ?? "" })
                  : ""}
              </p>
              {preview.manifest.description ? (
                <p className="mt-2 text-caption">{preview.manifest.description}</p>
              ) : null}
            </div>

            <Alert>
              <AlertCircle />
              <AlertTitle>{t(($) => $.plugins.consent.title)}</AlertTitle>
              <AlertDescription>{t(($) => $.plugins.consent.description)}</AlertDescription>
            </Alert>

            <ScopeList scopes={preview.scopes} highlighted={preview.added_scopes} />

            {scheduledHooks.length > 0 ? (
              <Alert>
                <CalendarClock />
                <AlertTitle>{t(($) => $.plugins.schedule.consent_title)}</AlertTitle>
                <AlertDescription>
                  {t(($) => $.plugins.schedule.consent_description)}
                  <ScheduleList hooks={scheduledHooks} />
                </AlertDescription>
              </Alert>
            ) : null}

            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => setPreview(null)}>
                {t(($) => $.plugins.consent.cancel)}
              </Button>
              <Button disabled={!canManage || installMutation.isPending} onClick={confirmInstall}>
                {installMutation.isPending ? <Loader2 className="animate-spin" /> : null}
                {preview.installed
                  ? t(($) => $.plugins.consent.confirm_upgrade)
                  : t(($) => $.plugins.consent.confirm)}
              </Button>
            </div>
          </div>
        ) : null}
      </SettingsCard>
    </SettingsSection>
  );
}

/** 单个已发布插件包：名称/标识 + 版本列表；可删除整个包，每个未安装版本可进入安装/升级评审。 */
function PublishedPackage({
  pluginPackage,
  canManage,
  busy,
  onReview,
  onDelete,
}: {
  pluginPackage: PluginPackage;
  canManage: boolean;
  busy: boolean;
  onReview: (versionId: string) => void;
  onDelete: (packageId: string) => void;
}) {
  const { t } = useT("settings");
  // 版本按新的在前返回，"已安装版本"靠标记而不是推断：发布之后它们是不同的行，
  // 而这一点正是"要不要升级"需要有人来做决定的原因。
  const installed = pluginPackage.versions.find((version) => version.installed === true);

  return (
    <div className="space-y-3 border-t border-surface-border px-4 py-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-body font-medium">{pluginPackage.name}</div>
          <p className="text-caption text-muted-foreground">{pluginPackage.plugin_key}</p>
        </div>
        <Button
          size="icon"
          variant="ghost"
          aria-label={t(($) => $.plugins.publish.delete)}
          disabled={!canManage || busy}
          onClick={() => onDelete(pluginPackage.id)}
        >
          <Trash2 />
        </Button>
      </div>

      <ul className="space-y-1.5">
        {pluginPackage.versions.map((version) => (
          <li key={version.id} className="flex flex-wrap items-center gap-2 text-caption">
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono">{version.version}</code>
            {version.installed === true ? (
              <Badge variant="secondary">{t(($) => $.plugins.publish.installed_version)}</Badge>
            ) : null}
            <span className="text-muted-foreground">{version.published_at.slice(0, 10)}</span>
            <span className="font-mono text-muted-foreground" title={version.digest}>
              {version.digest.slice(0, 12)}
            </span>
            {version.installed === true ? null : (
              <Button
                size="sm"
                variant="ghost"
                className="ml-auto"
                disabled={!canManage || busy}
                onClick={() => onReview(version.id)}
              >
                {installed ? t(($) => $.plugins.publish.review_upgrade) : t(($) => $.plugins.publish.review_install)}
              </Button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** 单个已安装插件的卡片：启停开关、卸载、授权 scope、定时任务活动、MCP 工具审批，以及清单生成的配置表单。 */
function InstalledPlugin({
  installation,
  wsId,
  canManage,
}: {
  installation: PluginInstallation;
  wsId: string;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const enabledMutation = useSetPluginEnabled(wsId);
  const uninstallMutation = useUninstallPlugin(wsId);
  const isMutating = enabledMutation.isPending || uninstallMutation.isPending;

  const reportError = (error: unknown) => {
    toast.error(error instanceof Error ? error.message : t(($) => $.plugins.action_failed));
  };

  // 只在插件确实贡献了 hook 时才渲染活动记录：没有 hook 的插件，自然也不会有
  // 调用失败可供上报。
  const hasHooks = (installation.hooks ?? []).length > 0;

  // mcp hook 在工具被批准之前是不生效的，所以只要存在就为每个 hook 都展示审批面板，
  // 而不是只在已经挂着某个待批项时才出现。
  const mcpContributions = mcpHooks(installation.hooks ?? []);
  const scheduledHooks = (installation.hooks ?? []).filter((hook) => hook.schedule !== undefined);

  const contributions = [
    ...installation.surfaces.map((surface) => `${surface.name} (${surface.type})`),
    ...installation.hooks.map((hook) => `${hook.name} (${hook.triggers.join(", ")})`),
    ...installation.resources.map((resource) => `${resource.key} (${resource.type})`),
  ];

  return (
    <SettingsCard>
      <div className="space-y-4 px-4 py-4">
        {hasHooks ? <PluginHookActivity wsId={wsId} installationId={installation.id} /> : null}
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="text-body font-semibold">{installation.name}</span>
              <Badge variant="secondary">{t(($) => $.plugins.version, { version: installation.version })}</Badge>
              {!installation.enabled ? (
                <Badge variant="outline">{t(($) => $.plugins.states.disabled)}</Badge>
              ) : null}
            </div>
            <p className="text-caption text-muted-foreground">{installation.plugin_key}</p>
            {installation.description ? (
              <p className="mt-2 max-w-2xl text-caption">{installation.description}</p>
            ) : null}
          </div>
          <div className="flex items-center gap-2">
            <Switch
              aria-label={t(($) => $.plugins.enabled_label)}
              disabled={!canManage || isMutating}
              checked={installation.enabled}
              onCheckedChange={(checked) => enabledMutation
                .mutateAsync({ installationId: installation.id, enabled: checked === true })
                .then(() => toast.success(checked
                  ? t(($) => $.plugins.enabled)
                  : t(($) => $.plugins.disabled)))
                .catch(reportError)}
            />
            <Button
              size="icon"
              variant="ghost"
              aria-label={t(($) => $.plugins.uninstall)}
              disabled={!canManage || isMutating}
              onClick={() => uninstallMutation.mutateAsync(installation.id)
                .then(() => toast.success(t(($) => $.plugins.uninstalled)))
                .catch(reportError)}
            >
              {uninstallMutation.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />}
            </Button>
          </div>
        </div>

        <div className="space-y-2">
          <div className="text-caption font-medium">{t(($) => $.plugins.granted_scopes)}</div>
          <ScopeList scopes={installation.granted_scopes} />
        </div>

        {scheduledHooks.length > 0 ? (
          <div className="space-y-1">
            <div className="flex items-center gap-2 text-caption font-medium">
              <CalendarClock className="size-4" />
              {t(($) => $.plugins.schedule.title)}
            </div>
            <ScheduleList hooks={scheduledHooks} showNextRun />
            <PluginScheduleActivity
              wsId={wsId}
              installationId={installation.id}
              hooks={scheduledHooks}
            />
          </div>
        ) : null}

        {mcpContributions.length > 0 ? (
          <div className="space-y-2">
            <div className="text-caption font-medium">{t(($) => $.plugins.mcp.badge)}</div>
            {mcpContributions.map((hook) => (
              <PluginMCPApproval
                key={hook.key}
                wsId={wsId}
                installationId={installation.id}
                hook={hook}
                canManage={canManage}
              />
            ))}
          </div>
        ) : null}

        {contributions.length > 0 ? (
          <div className="space-y-1">
            <div className="text-caption font-medium">{t(($) => $.plugins.contributes)}</div>
            <p className="text-caption text-muted-foreground">{contributions.join(" · ")}</p>
          </div>
        ) : null}

      </div>

      <ConfigForm installation={installation} canManage={canManage} wsId={wsId} />
    </SettingsCard>
  );
}

/**
 * 「插件」设置标签页：管理员可上传发布插件包、评审并安装/升级某个版本，管理已装插件的
 * 启停与卸载、授权 scope、定时任务和 MCP 工具审批；普通成员只能看到只读提示与已装列表。
 */
export function PluginsTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";

  const { data, isLoading, isError } = useQuery(pluginInstallationsOptions(wsId));
  const installations = useMemo(() => data?.plugins ?? [], [data]);

  return (
    <SettingsTab title={t(($) => $.plugins.title)} description={t(($) => $.plugins.description)}>
      {!canManage ? (
        <Alert>
          <AlertCircle />
          <AlertTitle>{t(($) => $.plugins.read_only)}</AlertTitle>
          <AlertDescription>{t(($) => $.plugins.read_only_description)}</AlertDescription>
        </Alert>
      ) : null}

      {canManage ? <PublishAndInstall wsId={wsId} canManage={canManage} /> : null}

      <SettingsSection title={t(($) => $.plugins.installed.title)}>
        {isLoading ? (
          <Skeleton className="h-24 w-full" aria-label={t(($) => $.plugins.loading)} />
        ) : isError ? (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.plugins.load_failed)}</AlertTitle>
            <AlertDescription>{t(($) => $.plugins.load_failed_description)}</AlertDescription>
          </Alert>
        ) : installations.length === 0 ? (
          <SettingsCard>
            <p className="px-4 py-6 text-caption text-muted-foreground">{t(($) => $.plugins.empty)}</p>
          </SettingsCard>
        ) : (
          <div className="space-y-4">
            {installations.map((installation) => (
              <InstalledPlugin
                key={installation.id}
                installation={installation}
                wsId={wsId}
                canManage={canManage}
              />
            ))}
          </div>
        )}
      </SettingsSection>
    </SettingsTab>
  );
}
