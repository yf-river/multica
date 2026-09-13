// 本文件是「Composio」集成面板：展示可接入的 Composio 工具包（toolkit，例如各种
// 外部 App 的能力）目录，让用户为 agent 连接 / 断开这些 App。

"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Loader2, Plug, RefreshCw, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
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
import { api } from "@multica/core/api";
import {
  composioConnectionsOptions,
  composioKeys,
  composioToolkitsOptions,
} from "@multica/core/composio";
import type { ComposioToolkit } from "@multica/core/types";
import { ComposioToolkitLogo } from "../../common/composio-toolkit-logo";
import { useT, useTimeAgo } from "../../i18n";
import { useNavigation } from "../../navigation";

/**
 * Composio 集成面板：渲染可连接的工具包目录，并支持连接 / 断开 agent 可操作的 App。
 *
 * 关键产品规则（MUL-4009）：服务端只会返回「在 Composio 项目里已配好鉴权配置」的
 * 工具包，所以这里的每张卡片都一定可连接——没有鉴权配置的工具包在服务端就被过滤掉
 * 了，不会出现一张死掉的「未配置」卡片。
 */
export function ComposioTab() {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const navigation = useNavigation();

  const toolkitsQuery = useQuery(composioToolkitsOptions());
  const connectionsQuery = useQuery(composioConnectionsOptions());

  const [query, setQuery] = useState("");
  const [connectingSlug, setConnectingSlug] = useState<string | null>(null);
  const [disconnectTarget, setDisconnectTarget] = useState<{
    connectionId: string;
    name: string;
  } | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  // Composio 的托管授权流程是一次整页跳转，授权完会带 `?connected=<slug>`（成功）
  // 或 `?error=composio_connect_failed`（任何服务端侧失败，见 Service.CallbackRedirect，
  // MUL-3720）回到设置页。这里把回调参数消费恰好一次：弹一个 toast、刷新连接列表
  // 让刚连上的卡片立刻翻成「已连接」（无需手动刷新），然后用 `replace` 清掉这一次性
  // 参数——否则用户刷新浏览器会重复弹 toast。
  const connectedParam = navigation.searchParams.get("connected");
  const errorParam = navigation.searchParams.get("error");
  // React 严格模式（开发 / Next）下挂载 effect 会以「挂载 → 清理 → 再挂载」执行两次：
  // 第二次执行时第一次的 `replace` 还没提交，闭包看到的仍是同一批参数，会把 toast
  // 和列表刷新各做两遍。这里用一个记录「已消费回调」的 ref 来挡重；真正的新回调
  // （不同 slug，或跳转本身就是会重置该 ref 的整页加载）仍会照常触发。
  const consumedCallbackKey = useRef<string | null>(null);
  useEffect(() => {
    const callbackKey = connectedParam
      ? `connected:${connectedParam}`
      : errorParam === "composio_connect_failed"
        ? "error:composio_connect_failed"
        : null;
    if (!callbackKey) return;
    if (consumedCallbackKey.current === callbackKey) return;
    consumedCallbackKey.current = callbackKey;
    if (connectedParam) {
      toast.success(t(($) => $.composio.toast_connected));
      void qc.invalidateQueries({ queryKey: composioKeys.connections() });
    } else {
      toast.error(t(($) => $.composio.toast_connect_failed));
    }
    // 只清掉 Composio 的一次性回调参数，保留其它参数（尤其是 ?tab=integrations），
    // 让用户停留在当前标签页。
    const params = new URLSearchParams(navigation.searchParams);
    params.delete("connected");
    params.delete("error");
    const qs = params.toString();
    navigation.replace(qs ? `${navigation.pathname}?${qs}` : navigation.pathname);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectedParam, errorParam]);

  // 按工具包 slug 归集有效连接，让每张卡片知道自己是「已连接」（以及该断开哪个连接）。
  const connectionBySlug = useMemo(() => {
    const m = new Map<string, string>();
    for (const c of connectionsQuery.data ?? []) {
      if (c.status === "active") m.set(c.toolkit_slug, c.id);
    }
    return m;
  }, [connectionsQuery.data]);

  // 最近一次连接已过期的工具包，卡片上会显示「重新连接」而不是「已连接 / 连接」。
  // 服务端要等第 4 阶段（MUL-3719）落地后才会真的下发 `expired`，但这个分支现在
  // 就接好了，届时自动生效。
  const expiredBySlug = useMemo(() => {
    const m = new Set<string>();
    for (const c of connectionsQuery.data ?? []) {
      if (c.status === "expired") m.add(c.toolkit_slug);
    }
    return m;
  }, [connectionsQuery.data]);

  // 每个有效连接的「最近使用」时间，用于已连接卡片上的「上次使用 …」行。服务端要等
  // 工具调用分发开始打时间戳（第 3 阶段，MUL-3721）才有值；在那之前卡片显示「从未
  // 使用」占位。
  const lastUsedBySlug = useMemo(() => {
    const m = new Map<string, string | null>();
    for (const c of connectionsQuery.data ?? []) {
      if (c.status === "active") m.set(c.toolkit_slug, c.last_used_at ?? null);
    }
    return m;
  }, [connectionsQuery.data]);

  const toolkits = useMemo(() => toolkitsQuery.data ?? [], [toolkitsQuery.data]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return toolkits;
    return toolkits.filter(
      (tk) =>
        tk.name.toLowerCase().includes(q) ||
        tk.slug.toLowerCase().includes(q) ||
        (tk.category ?? "").toLowerCase().includes(q),
    );
  }, [toolkits, query]);

  // 503 的处理在父级 IntegrationsTab：COMPOSIO_API_KEY 没配置时整个 Composio 分区被
  // 隐藏——也就是说本组件只有在集成已配置时才会被挂载，所以这里只需面对「加载中 /
  // 出错 / 空列表 / 列表」几种展示状态。

  async function handleConnect(tk: ComposioToolkit) {
    if (connectingSlug) return;
    setConnectingSlug(tk.slug);
    try {
      const { redirect_url } = await api.beginComposioConnect(tk.slug);
      // 把浏览器交给 Composio 的托管授权流程；完成后会跳回 /api/integrations/composio/callback。
      window.location.href = redirect_url;
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.composio.connect_failed));
      setConnectingSlug(null);
    }
  }

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteComposioConnection(disconnectTarget.connectionId);
      await qc.invalidateQueries({ queryKey: composioKeys.connections() });
      toast.success(t(($) => $.composio.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.composio.disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div className="space-y-6">
      <section className="space-y-1">
        <p className="text-body text-muted-foreground">{t(($) => $.composio.page_description)}</p>
      </section>

      {toolkitsQuery.isLoading ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">{t(($) => $.composio.loading)}</p>
          </CardContent>
        </Card>
      ) : toolkitsQuery.isError ? (
        <Card>
          <CardContent>
            <p className="text-body text-destructive">{t(($) => $.composio.load_failed)}</p>
          </CardContent>
        </Card>
      ) : toolkits.length === 0 ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.composio.empty_title)}</p>
            <p className="text-caption text-muted-foreground">{t(($) => $.composio.empty_description)}</p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t(($) => $.composio.search_placeholder)}
            className="max-w-xs"
          />
          {connectionsQuery.isError && (
            // 不要默默把「连接列表拉取失败」当成「没连任何东西」——那会掩盖真实已存在的
            // 连接，并在某个其实已连过的应用上给出「连接」按钮。把错误摆出来，让用户知道
            // 已连接状态可能不完整；目录本身照常渲染。
            <p className="text-caption text-destructive">
              {t(($) => $.composio.connections_load_failed)}
            </p>
          )}
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {filtered.map((tk) => (
              <ToolkitCard
                key={tk.slug}
                toolkit={tk}
                connectionId={connectionBySlug.get(tk.slug)}
                expired={expiredBySlug.has(tk.slug)}
                lastUsedAt={lastUsedBySlug.get(tk.slug) ?? null}
                connecting={connectingSlug === tk.slug}
                anyConnecting={connectingSlug !== null}
                onConnect={() => handleConnect(tk)}
                onDisconnect={(connectionId, name) =>
                  setDisconnectTarget({ connectionId, name })
                }
              />
            ))}
          </div>
        </section>
      )}

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.composio.disconnect_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.composio.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.composio.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.composio.disconnecting)
                : t(($) => $.composio.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * 单个工具包卡片：左边 logo + 名称（下方显示分类或最近使用时间），右侧按连接状态
 * 给出三种动作——已连接则显示绿色「已连接」+ 断开按钮；连接过期则黄色警示 + 一键
 * 重新连接；未连接则给出「连接」按钮。
 */
function ToolkitCard({
  toolkit,
  connectionId,
  expired,
  lastUsedAt,
  connecting,
  anyConnecting,
  onConnect,
  onDisconnect,
}: {
  toolkit: ComposioToolkit;
  connectionId?: string;
  expired: boolean;
  lastUsedAt: string | null;
  connecting: boolean;
  anyConnecting: boolean;
  onConnect: () => void;
  onDisconnect: (connectionId: string, name: string) => void;
}) {
  const { t } = useT("settings");
  const timeAgo = useTimeAgo();
  const isConnected = !!connectionId;

  return (
    <Card>
      <CardContent className="flex items-center gap-3 p-3">
        <ComposioToolkitLogo
          slug={toolkit.slug}
          name={toolkit.name}
          fallbackLogo={toolkit.logo}
        />
        <div className="min-w-0 flex-1">
          <p className="truncate text-body font-medium">{toolkit.name || toolkit.slug}</p>
          {isConnected ? (
            // 最近使用行。服务端要等第 3 阶段分发打时间戳后才会有 last_used_at；
            // 在它出现之前显示本地化的「从未使用」占位，而不是整行消失。
            <p className="truncate text-micro text-muted-foreground">
              {lastUsedAt
                ? t(($) => $.composio.last_used, { when: timeAgo(lastUsedAt) })
                : t(($) => $.composio.last_used_never)}
            </p>
          ) : toolkit.category ? (
            <p className="truncate text-micro uppercase tracking-wide text-muted-foreground">
              {toolkit.category}
            </p>
          ) : null}
        </div>

        {isConnected ? (
          <div className="flex items-center gap-2">
            <span className="inline-flex items-center gap-1 text-caption text-emerald-600">
              <Check className="h-3 w-3" />
              {t(($) => $.composio.connected)}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={() => onDisconnect(connectionId!, toolkit.name || toolkit.slug)}
              aria-label={t(($) => $.composio.disconnect)}
            >
              <Trash2 className="h-3 w-3" />
            </Button>
          </div>
        ) : expired ? (
          // 令牌已过期的连接：把失败摆出来，让用户一步重走同样的连接流程（无需先断开）。
          <div className="flex items-center gap-2">
            <span className="inline-flex items-center gap-1 text-caption text-amber-600">
              <AlertTriangle className="h-3 w-3" />
              {t(($) => $.composio.expired)}
            </span>
            <Button size="sm" variant="outline" onClick={onConnect} disabled={anyConnecting}>
              {connecting ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : (
                <RefreshCw className="h-3 w-3" />
              )}
              {connecting ? t(($) => $.composio.connecting) : t(($) => $.composio.reconnect)}
            </Button>
          </div>
        ) : (
          <Button size="sm" onClick={onConnect} disabled={anyConnecting}>
            {connecting ? (
              <Loader2 className="h-3 w-3 animate-spin" />
            ) : (
              <Plug className="h-3 w-3" />
            )}
            {connecting ? t(($) => $.composio.connecting) : t(($) => $.composio.connect)}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
