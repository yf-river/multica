// Webhook 触发载荷预览：把服务器端 normalizeWebhookPayload 产出的 WebhookEnvelope
// 形状内联展示在 autopilot 运行详情里。默认折叠、点击展开，展开后展示事件名、
// 接收时间、content-type 与格式化的 JSON（超长截断显示，复制仍给完整内容）。
// 刻意只读且不绑定任何具体弹窗——它被放进 AgentTranscriptDialog 的 headerSlot。

"use client";

import { useState, useMemo } from "react";
import { Webhook, ChevronDown, ChevronRight, Copy, Check } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";

interface WebhookPayloadPreviewProps {
  payload: unknown;
  /** 默认展开还是折叠。弹窗纵向空间有限，所以默认折叠、由用户展开。 */
  defaultOpen?: boolean;
}

/**
 * Webhook 触发载荷预览。若载荷不是信封结构（调用方直接写了 trigger_payload、
 * 历史行损坏等），回退为把整个载荷当作 eventPayload 展示，任何内容都不隐藏。
 * @param payload 服务器侧产生的触发载荷（WebhookEnvelope 或任意 JSON）。
 * @param defaultOpen 默认是否展开。
 */
export function WebhookPayloadPreview({
  payload,
  defaultOpen = false,
}: WebhookPayloadPreviewProps) {
  const { t } = useT("autopilots");
  const [open, setOpen] = useState(defaultOpen);
  const [copied, setCopied] = useState(false);

  // 从信封结构里提取展示字段：event 名、eventPayload 主体、request.receivedAt 与
  // request.contentType。信封里没有的字段保持 null、不在界面渲染。
  const { event, receivedAt, contentType, fullJSON, displayJSON, isTruncated } = useMemo(() => {
    let event: string | null = null;
    let eventPayload: unknown = null;
    let receivedAt: string | null = null;
    let contentType: string | null = null;
    if (payload && typeof payload === "object" && !Array.isArray(payload)) {
      const obj = payload as Record<string, unknown>;
      if (typeof obj.event === "string") event = obj.event;
      if ("eventPayload" in obj) eventPayload = obj.eventPayload;
      const req = obj.request;
      if (req && typeof req === "object") {
        const r = req as Record<string, unknown>;
        if (typeof r.receivedAt === "string") receivedAt = r.receivedAt;
        if (typeof r.contentType === "string") contentType = r.contentType;
      }
    }
    // 载荷没匹配信封结构时（调用方直接写了 trigger_payload、历史行损坏等），把整
    // 个东西当 eventPayload 展示，任何内容都不藏。
    if (eventPayload === null && payload !== null && payload !== undefined) {
      eventPayload = payload;
    }
    const fullJSON = JSON.stringify(eventPayload, null, 2);
    // 截断进 DOM 的字符串，让弹窗在 provider 发了 256 KiB 信封时依然流畅。复制
    // 按钮仍产出完整字符串，用户绝不丢数据。4 KiB 足以展示信封头 + 典型 webhook
    // 载荷的首批对象级字段。
    const TRUNCATE_AT = 4096;
    const isTruncated = fullJSON.length > TRUNCATE_AT;
    const displayJSON = isTruncated ? fullJSON.slice(0, TRUNCATE_AT) : fullJSON;
    return { event, receivedAt, contentType, fullJSON, displayJSON, isTruncated };
  }, [payload]);

  const handleCopy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (await copyText(fullJSON)) {
      setCopied(true);
      toast.success(t(($) => $.webhook_payload.copied));
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error(t(($) => $.webhook_payload.copy_failed));
    }
  };

  return (
    <div className="rounded-md border bg-background">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-caption hover:bg-accent/30 transition-colors"
      >
        <Webhook className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium">
          {t(($) => $.webhook_payload.label)}
        </span>
        <code className="truncate font-mono text-muted-foreground">
          {event ?? t(($) => $.webhook_payload.unknown_event)}
        </code>
        {receivedAt && (
          <span className="ml-auto shrink-0 text-muted-foreground">
            {receivedAt}
          </span>
        )}
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        )}
      </button>
      {open && (
        <div className="border-t">
          <div className="flex items-center justify-between px-3 py-1.5 text-micro text-muted-foreground">
            <span>
              {contentType
                ? t(($) => $.webhook_payload.content_type, { type: contentType })
                : t(($) => $.webhook_payload.payload)}
            </span>
            <button
              type="button"
              onClick={handleCopy}
              className={cn(
                "flex items-center gap-1 rounded px-2 py-0.5 hover:bg-accent transition-colors",
              )}
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
          <pre className="max-h-64 overflow-auto bg-muted/40 px-3 py-2 text-caption font-mono leading-relaxed">
            {displayJSON}
            {isTruncated && (
              <span className="block pt-2 text-muted-foreground">
                {t(($) => $.webhook_payload.truncated_marker)}
              </span>
            )}
          </pre>
        </div>
      )}
    </div>
  );
}
