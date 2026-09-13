// Webhook URL 展示字段：默认把 token 遮住，只在用户显式要求时才在 DOM 里出现
// 明文。URL 是 bearer 凭据——任何从屏幕共享或截图读到它的人都能触发 autopilot，
// 所以渲染的是掩码形态；纯 CSS 模糊做不到这一点。

"use client";

import { useState, type ReactNode } from "react";
import { Check, Copy, Eye, EyeOff } from "lucide-react";
import { toast } from "sonner";
import { maskAutopilotWebhookUrl } from "@multica/core/autopilots";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";

// 两种尺寸形态：sm 用于详情页触发行的行内紧凑样式；md 用于创建后弹窗里的独立
// 大字字段。
const SIZES = {
  // 详情页触发行——紧凑，与行动作排在一行内。
  sm: {
    row: "items-center",
    value: "rounded bg-muted px-2 py-1 text-caption",
    button: "h-7 w-7",
    buttonVariant: "ghost",
    icon: "h-3.5 w-3.5",
  },
  // 创建后面板——更大的独立字段。
  md: {
    row: "items-stretch",
    value: "rounded-md border bg-muted px-3 py-2 text-caption",
    button: "h-9 w-9",
    buttonVariant: "outline",
    icon: "size-4",
  },
} as const;

interface WebhookUrlFieldProps {
  url: string;
  size?: keyof typeof SIZES;
  /** Copy 之后渲染的附加控件（轮换、删除）。 */
  actions?: ReactNode;
}

/**
 * 默认隐藏 token 的 Webhook URL 展示。
 *
 * URL 是 bearer 凭据：任何人从屏幕共享或截图看到它都能触发该 autopilot。所以
 * 渲染的是掩码形态——明文 token 在用户要求前绝不进 DOM，纯 CSS 模糊给不了这一
 * 层保证。隐藏时复制照常可用，常见场景根本不需要揭示。
 * @param url 完整 webhook URL。
 * @param size 尺寸档位（sm/md）。
 * @param actions 附加操作（如轮换密钥、删除）。
 */
export function WebhookUrlField({ url, size = "sm", actions }: WebhookUrlFieldProps) {
  const { t } = useT("autopilots");
  // 揭示授权的是某一个具体的 URL，不是这个字段。轮换 token 会在挂载着的行下面
  // 换掉 `url`，新凭据必须再次被单独揭示——裸布尔会把旧授权送给新密钥，恰恰在
  // 用户为遏制泄露而轮换的那一刻把它暴露出来。渲染期间推导（而非在 effect 里
  // 重置）意味着新 token 从不进入 DOM，包括 effect 前的那一帧。
  const [revealedUrl, setRevealedUrl] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const revealed = revealedUrl === url;
  const s = SIZES[size];

  const handleCopy = async () => {
    if (!url) return;
    if (await copyText(url)) {
      setCopied(true);
      toast.success(t(($) => $.trigger_row.url_copied));
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error(t(($) => $.trigger_row.url_copy_failed));
    }
  };

  const valueClassName = cn(
    "flex-1 min-w-0 truncate font-mono text-foreground",
    s.value,
  );

  return (
    <div className={cn("flex gap-1.5", s.row)}>
      {revealed ? (
        <code className={valueClassName}>{url}</code>
      ) : (
        // 点击揭示就在值本身。揭示后变回纯文本让 URL 可拖选——那里若放个切换
        // 按钮会吞掉拖选。
        <button
          type="button"
          onClick={() => setRevealedUrl(url)}
          className={cn(valueClassName, "text-left cursor-pointer transition-colors hover:bg-muted/70")}
          title={t(($) => $.trigger_row.show_url)}
          aria-label={t(($) => $.trigger_row.hidden_url_aria)}
        >
          {maskAutopilotWebhookUrl(url)}
        </button>
      )}
      <Button
        size="icon"
        variant={s.buttonVariant}
        className={cn("shrink-0", s.button)}
        onClick={() => setRevealedUrl(revealed ? null : url)}
        title={revealed ? t(($) => $.trigger_row.hide_url) : t(($) => $.trigger_row.show_url)}
        aria-label={revealed ? t(($) => $.trigger_row.hide_url) : t(($) => $.trigger_row.show_url)}
      >
        {revealed ? (
          <EyeOff className={cn(s.icon, "text-muted-foreground")} />
        ) : (
          <Eye className={cn(s.icon, "text-muted-foreground")} />
        )}
      </Button>
      <Button
        size="icon"
        variant={s.buttonVariant}
        className={cn("shrink-0", s.button)}
        onClick={handleCopy}
        title={t(($) => $.trigger_row.copy_url)}
        aria-label={t(($) => $.trigger_row.copy_url)}
      >
        {copied ? (
          <Check className={cn(s.icon, "text-emerald-500")} />
        ) : (
          <Copy className={cn(s.icon, "text-muted-foreground")} />
        )}
      </Button>
      {actions}
    </div>
  );
}
