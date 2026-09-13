// Webhook 投递记录区：列出某 autopilot 收到的所有 webhook 投递（每行状态/来源/
// 事件/时间），点行进详情弹窗查看原始请求体、选中的请求头、响应体与元信息，
// 并可在服务端允许时"重放"某次投递。仅当该 autopilot 配了 webhook 触发时才渲染。

"use client";

import { useState } from "react";
import {
  CheckCircle2,
  XCircle,
  Loader2,
  Ban,
  AlertTriangle,
  ShieldOff,
  RotateCw,
  Copy,
  Check,
  Webhook,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  autopilotDeliveriesOptions,
  autopilotDeliveryOptions,
  useReplayAutopilotDelivery,
} from "@multica/core/autopilots";
import { useWorkspaceId } from "@multica/core/hooks";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { toast } from "sonner";
import { useLocale, useT } from "../../i18n";
import type {
  WebhookDelivery,
  WebhookDeliveryStatus,
  WebhookSignatureStatus,
} from "@multica/core/types";

// --- 状态视觉 ──────────────────────────────────────────────────────────────

// 映射对当前后端枚举是穷尽的，但每个消费点都在服务器新增取值时回退到通用
// "未知"视觉——见 CLAUDE.md 的 API Response Compatibility 规则。
type StatusVisual = {
  color: string;
  icon: typeof CheckCircle2;
  spin?: boolean;
};

const STATUS_VISUAL: Record<WebhookDeliveryStatus, StatusVisual> = {
  queued: { color: "text-blue-500", icon: Loader2, spin: true },
  dispatched: { color: "text-emerald-500", icon: CheckCircle2 },
  // 签名失败与预检退回都落在这里。视觉上读作失败，弹窗底部说明原因。
  rejected: { color: "text-destructive", icon: ShieldOff },
  // ignored 覆盖已暂停/禁用/归档的 autopilot——同样的载荷收到了，但没有创建运行。
  // 用弱化色，让它看起来不像 bug。
  ignored: { color: "text-muted-foreground", icon: Ban },
  failed: { color: "text-destructive", icon: XCircle },
};

// 未知状态的回退视觉（服务器将来可能加新枚举值）。
const UNKNOWN_VISUAL: StatusVisual = {
  color: "text-muted-foreground",
  icon: AlertTriangle,
};

// 按状态取视觉；未知状态落到回退。
function visualForStatus(status: string): StatusVisual {
  return (STATUS_VISUAL as Record<string, StatusVisual>)[status] ?? UNKNOWN_VISUAL;
}

// --- 辅助函数 ──────────────────────────────────────────────────────────────

// 时间格式化：空值显示破折号。
function formatDate(value: string, locale: string): string {
  if (!value) return "—";
  return new Date(value).toLocaleString(locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// 某次投递能否重放：(a) 服务端允许（签名不是 invalid 且投递本身未被 rejected），
// (b) 有可重放的东西（raw_body / received）。镜像服务器的规则而不依赖响应——
// 保持按钮禁用能省一次 400 往返。
function canReplay(delivery: WebhookDelivery): boolean {
  if (delivery.signature_status === "invalid") return false;
  if (delivery.status === "rejected") return false;
  // `queued` 投递在服务端飞行中；此时重放会与同步分发路径竞态。落定后用户可重放。
  if (delivery.status === "queued") return false;
  return true;
}

// --- 投递列表区 ────────────────────────────────────────────────────────────

/**
 * Webhook 投递记录区。没有配置 webhook 触发时整节不渲染（保持仅定时型 autopilot
 * 的详情页简短），而不是画一张空卡。
 * @param autopilotId autopilot 的 ID。
 * @param hasWebhookTrigger 是否配了 webhook 触发——决定数据查询是否启用与本节是否可见。
 */
export function WebhookDeliveriesSection({
  autopilotId,
  hasWebhookTrigger,
}: {
  autopilotId: string;
  hasWebhookTrigger: boolean;
}) {
  const { t } = useT("autopilots");
  const wsId = useWorkspaceId();

  const { data: deliveries = [], isLoading } = useQuery(
    autopilotDeliveriesOptions(wsId, autopilotId, {
      enabled: hasWebhookTrigger,
    }),
  );

  // 没有 webhook 触发 → 整节都不相关。隐藏而非渲染空卡，让仅定时型 autopilot 的
  // 详情页保持简短。
  if (!hasWebhookTrigger) return null;

  return (
    <section className="space-y-3">
      <h2 className="text-body font-medium text-muted-foreground uppercase tracking-wider">
        {t(($) => $.deliveries.section_title)}
      </h2>
      {isLoading ? (
        <div className="space-y-1">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : deliveries.length === 0 ? (
        <div className="rounded-md border border-dashed p-4 text-center text-body text-muted-foreground">
          {t(($) => $.deliveries.empty)}
        </div>
      ) : (
        <div className="rounded-md border overflow-hidden">
          {deliveries.map((delivery) => (
            <DeliveryRow
              key={delivery.id}
              delivery={delivery}
              autopilotId={autopilotId}
            />
          ))}
        </div>
      )}
    </section>
  );
}

// --- 列表行 ────────────────────────────────────────────────────────────────

// 单行投递记录：状态图标/文案、来源 provider、事件名、重放与尝试徽标、接收时间；
// 点行打开详情弹窗。
function DeliveryRow({
  delivery,
  autopilotId,
}: {
  delivery: WebhookDelivery;
  autopilotId: string;
}) {
  const { t } = useT("autopilots");
  const locale = useLocale();
  const [open, setOpen] = useState(false);

  const visual = visualForStatus(delivery.status);
  const StatusIcon = visual.icon;
  const statusLabel =
    t(($) => $.deliveries.status[delivery.status as WebhookDeliveryStatus]) ??
    delivery.status;
  const providerLabel = delivery.provider || "—";

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-body hover:bg-accent/30 transition-colors"
      >
        <StatusIcon
          className={cn(
            "h-4 w-4 shrink-0",
            visual.color,
            visual.spin && "animate-spin",
          )}
        />
        <span className={cn("w-24 shrink-0 text-caption font-medium", visual.color)}>
          {statusLabel}
        </span>
        <span className="w-20 shrink-0 text-caption text-muted-foreground truncate">
          {providerLabel}
        </span>
        <span className="flex-1 min-w-0 text-caption text-muted-foreground truncate font-mono">
          {delivery.event || t(($) => $.webhook_payload.unknown_event)}
        </span>
        {delivery.replayed_from_delivery_id && (
          <Badge variant="secondary" className="shrink-0">
            <RotateCw className="h-3 w-3" />
            {t(($) => $.deliveries.row.replay_badge)}
          </Badge>
        )}
        {delivery.attempt_count > 1 && (
          <Badge variant="outline" className="shrink-0">
            {t(($) => $.deliveries.row.attempts, {
              count: delivery.attempt_count,
            })}
          </Badge>
        )}
        <span className="w-32 shrink-0 text-right text-caption text-muted-foreground tabular-nums">
          {formatDate(delivery.received_at || delivery.created_at, locale)}
        </span>
      </button>
      {open && (
        <DeliveryDetailDialog
          open={open}
          onOpenChange={setOpen}
          autopilotId={autopilotId}
          delivery={delivery}
        />
      )}
    </>
  );
}

// --- 详情弹窗 ──────────────────────────────────────────────────────────────

// 单次投递的详情：状态头部（含签名徽标）、元信息网格、错误、正文区（请求体/
// 选中头/响应体，懒加载）+ 重放按钮。正文较大时滚动，保证"重放"等底部内容可达。
function DeliveryDetailDialog({
  open,
  onOpenChange,
  autopilotId,
  delivery,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  autopilotId: string;
  delivery: WebhookDelivery;
}) {
  const { t } = useT("autopilots");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const { data: detail, isLoading } = useQuery(
    autopilotDeliveryOptions(wsId, autopilotId, delivery.id, { enabled: open }),
  );
  // 详情已加载时用详情行，否则用列表里的精简行。精简行缺 raw_body /
  // response_body / selected_headers；详情还在加载时，弹窗为这些区渲染骨架占位。
  const full = detail ?? delivery;
  const visual = visualForStatus(full.status);
  const StatusIcon = visual.icon;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* max-h + overflow-y-auto：webhook 请求体 + 头 + 响应很容易超过视口高。
          不加帽的话弹窗会长出屏幕边缘、底部（如重放按钮）够不到。85vh 给弹窗四周
          留出呼吸空间。 */}
      <DialogContent className="max-w-2xl max-h-[85vh] overflow-y-auto">
        <DialogTitle className="flex items-center gap-2">
          <Webhook className="h-4 w-4 text-muted-foreground" />
          {t(($) => $.deliveries.detail.title)}
        </DialogTitle>
        <div className="space-y-4 pt-1">
          {/* 头部行——状态 / provider / 事件 */}
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <StatusIcon
                className={cn(
                  "h-4 w-4 shrink-0",
                  visual.color,
                  visual.spin && "animate-spin",
                )}
              />
              <span className={cn("text-body font-medium", visual.color)}>
                {t(($) => $.deliveries.status[full.status as WebhookDeliveryStatus]) ??
                  full.status}
              </span>
            </div>
            <Badge variant="outline">{full.provider || "—"}</Badge>
            <code className="rounded bg-muted px-2 py-0.5 text-caption font-mono">
              {full.event || t(($) => $.webhook_payload.unknown_event)}
            </code>
            <SignatureBadge status={full.signature_status as WebhookSignatureStatus} />
          </div>

          {/* 元信息网格 */}
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-caption">
            <MetaRow
              label={t(($) => $.deliveries.detail.received_at)}
              value={formatDate(full.received_at, locale)}
            />
            <MetaRow
              label={t(($) => $.deliveries.detail.last_attempt_at)}
              value={formatDate(full.last_attempt_at, locale)}
            />
            <MetaRow
              label={t(($) => $.deliveries.detail.attempt_count)}
              value={String(full.attempt_count)}
            />
            <MetaRow
              label={t(($) => $.deliveries.detail.dispatch_attempts)}
              value={String(full.dispatch_attempts)}
            />
            {full.status === "queued" && (
              <MetaRow
                label={t(($) => $.deliveries.detail.available_at)}
                value={formatDate(full.available_at, locale)}
              />
            )}
            <MetaRow
              label={t(($) => $.deliveries.detail.response_status)}
              value={full.response_status != null ? String(full.response_status) : "—"}
            />
            <MetaRow
              label={t(($) => $.deliveries.detail.dedupe_key)}
              value={full.dedupe_key ?? "—"}
              mono
            />
            <MetaRow
              label={t(($) => $.deliveries.detail.dedupe_source)}
              value={full.dedupe_source ?? "—"}
            />
            {full.content_type && (
              <MetaRow
                label={t(($) => $.deliveries.detail.content_type)}
                value={full.content_type}
                mono
              />
            )}
            {full.replayed_from_delivery_id && (
              <MetaRow
                label={t(($) => $.deliveries.detail.replayed_from)}
                value={full.replayed_from_delivery_id}
                mono
              />
            )}
          </dl>

          {full.error && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-caption text-destructive">
              <div className="font-medium">
                {t(($) => $.deliveries.detail.error_label)}
              </div>
              <div className="mt-0.5 font-mono break-all">{full.error}</div>
            </div>
          )}

          {/* 原始请求体 + 响应体 + 请求头，全部懒加载 */}
          <DetailSections detail={detail} isLoading={isLoading} />

          {/* 重放按钮 */}
          <div className="flex items-center justify-between pt-2">
            <ReplayHint delivery={full} />
            <ReplayButton
              autopilotId={autopilotId}
              delivery={full}
              onSuccess={() => onOpenChange(false)}
            />
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// 元信息网格里的一行：标签在上、值在下（可选等宽），长值截断并带悬浮完整提示。
function MetaRow({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="flex flex-col">
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          "truncate text-foreground",
          mono && "font-mono",
        )}
        title={value}
      >
        {value}
      </dd>
    </div>
  );
}

// 签名状态徽标：valid → 默认强调；invalid → 危险；missing → 次要；未知 → outline。
function SignatureBadge({ status }: { status: WebhookSignatureStatus | string }) {
  const { t } = useT("autopilots");
  let variant: "default" | "secondary" | "destructive" | "outline" = "outline";
  if (status === "valid") variant = "default";
  else if (status === "invalid") variant = "destructive";
  else if (status === "missing") variant = "secondary";
  return (
    <Badge variant={variant}>
      {t(($) => $.deliveries.signature[status as WebhookSignatureStatus]) ?? status}
    </Badge>
  );
}

// 正文区：原始请求体 / 选中的请求头 / 响应体。详情还在加载且无缓存时给骨架。
function DetailSections({
  detail,
  isLoading,
}: {
  detail: WebhookDelivery | undefined;
  isLoading: boolean;
}) {
  const { t } = useT("autopilots");
  if (isLoading && !detail) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    );
  }
  if (!detail) return null;
  return (
    <div className="space-y-3">
      {detail.raw_body && (
        <CodeBlock
          label={t(($) => $.deliveries.detail.raw_body)}
          value={detail.raw_body}
        />
      )}
      {detail.selected_headers && Object.keys(detail.selected_headers).length > 0 && (
        <CodeBlock
          label={t(($) => $.deliveries.detail.selected_headers)}
          value={JSON.stringify(detail.selected_headers, null, 2)}
        />
      )}
      {detail.response_body && (
        <CodeBlock
          label={t(($) => $.deliveries.detail.response_body)}
          value={detail.response_body}
        />
      )}
    </div>
  );
}

// 一段带标题、可复制、超长截断显示的代码块。
function CodeBlock({ label, value }: { label: string; value: string }) {
  const { t } = useT("autopilots");
  const [copied, setCopied] = useState(false);
  // 超大体量的正文只截断进 DOM 的显示；复制按钮仍给出完整字符串。4 KiB 对典型
  // webhook 载荷足够大，同时保持弹窗流畅。
  const TRUNCATE_AT = 4096;
  const isTruncated = value.length > TRUNCATE_AT;
  const display = isTruncated ? value.slice(0, TRUNCATE_AT) : value;

  const handleCopy = async () => {
    if (await copyText(value)) {
      setCopied(true);
      toast.success(t(($) => $.webhook_payload.copied));
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error(t(($) => $.webhook_payload.copy_failed));
    }
  };

  return (
    // min-w-0 让这张卡可缩到 <pre> 固有 min-content 宽度以下——否则一段压缩的
    // 单行 JSON 会把外层 grid/flex 单元格（连同整个 DialogContent）推出视口边缘。
    <div className="min-w-0 rounded-md border bg-background">
      <div className="flex items-center justify-between border-b px-3 py-1.5 text-micro">
        <span className="font-medium text-muted-foreground">{label}</span>
        <button
          type="button"
          onClick={handleCopy}
          className="flex items-center gap-1 rounded px-2 py-0.5 hover:bg-accent transition-colors"
        >
          {copied ? (
            <Check className="h-3 w-3 text-emerald-500" />
          ) : (
            <Copy className="h-3 w-3" />
          )}
          {copied
            ? t(($) => $.webhook_payload.copied_short)
            : t(($) => $.webhook_payload.copy)}
        </button>
      </div>
      {/* whitespace-pre-wrap 保留美化打印的缩进但让长行回绕；break-all 是唯一能在
          token 中段断行的手段（压缩 JSON 没有空白可断）。 */}
      <pre className="max-h-48 overflow-auto bg-muted/40 px-3 py-2 text-caption font-mono leading-relaxed whitespace-pre-wrap break-all">
        {display}
        {isTruncated && (
          <span className="block pt-2 text-muted-foreground">
            {t(($) => $.webhook_payload.truncated_marker)}
          </span>
        )}
      </pre>
    </div>
  );
}

// 重放按钮左侧的解释文案：为什么不能重放（签名无效/被拒/仍在队列）。
function ReplayHint({ delivery }: { delivery: WebhookDelivery }) {
  const { t } = useT("autopilots");
  if (delivery.signature_status === "invalid") {
    return (
      <span className="text-caption text-muted-foreground">
        {t(($) => $.deliveries.replay.disabled_invalid_signature)}
      </span>
    );
  }
  if (delivery.status === "rejected") {
    return (
      <span className="text-caption text-muted-foreground">
        {t(($) => $.deliveries.replay.disabled_rejected)}
      </span>
    );
  }
  if (delivery.status === "queued") {
    return (
      <span className="text-caption text-muted-foreground">
        {t(($) => $.deliveries.replay.disabled_queued)}
      </span>
    );
  }
  return null;
}

// 重放按钮：把该次投递重新派发一次。可重放性见 canReplay；成功 toast 后关闭弹窗。
function ReplayButton({
  autopilotId,
  delivery,
  onSuccess,
}: {
  autopilotId: string;
  delivery: WebhookDelivery;
  onSuccess: () => void;
}) {
  const { t } = useT("autopilots");
  const replay = useReplayAutopilotDelivery();
  const enabled = canReplay(delivery) && !replay.isPending;

  const handleClick = async () => {
    try {
      await replay.mutateAsync({ autopilotId, deliveryId: delivery.id });
      toast.success(t(($) => $.deliveries.replay.toast_success));
      onSuccess();
    } catch (e: unknown) {
      const message =
        e instanceof Error
          ? e.message
          : t(($) => $.deliveries.replay.toast_failed);
      toast.error(message);
    }
  };

  return (
    <Button
      size="sm"
      variant="outline"
      onClick={handleClick}
      disabled={!enabled}
    >
      <RotateCw
        className={cn(
          "h-3.5 w-3.5 mr-1",
          replay.isPending && "animate-spin",
        )}
      />
      {replay.isPending
        ? t(($) => $.deliveries.replay.in_progress)
        : t(($) => $.deliveries.replay.action)}
    </Button>
  );
}
