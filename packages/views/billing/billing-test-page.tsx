"use client";

// 测试用的计费页。把 /api/cloud-billing/* 的每个接口都塞进一个屏幕，以便在
// 还没有正式 UI 前端到端验证代理 + Stripe 流程。区块：
//
//   1. 余额卡片
//   2. Stripe 成功横幅（仅当 URL 携带 ?session_id=... 时可见——用户刚从
//      Stripe Checkout 回来，我们轮询上游直到充值进入终态）。
//   3. 购买区：以服务端价格为权威的档位按钮，POST 一个 checkout-session 后
//      window.location.href = url 跳转。
//   4. Billing Portal 按钮。
//   5. 三个列表：交易记录 / 批次 / 充值记录。
//
// 超过「让 API 能对话 Stripe 并把结果展示出来」的部分刻意不在本页范围——
// 等真正的计费 UI 上线它会另居他处，这一整页都可以删掉。文案仍然走 useT()，
// 以通过包级 i18next/no-literal-string lint 规则；命名空间是 `billing`，
// en/zh-Hans 语言包与其它命名空间并排放置。真正的 UI 落地时，命名空间和本
// 文件会一起被删除。

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, RefreshCw, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import {
  billingBalanceOptions,
  billingBatchesOptions,
  billingCheckoutSessionOptions,
  billingPriceTiersOptions,
  billingTopupsOptions,
  billingTransactionsOptions,
  useCreateCloudBillingCheckoutSession,
  useCreateCloudBillingPortalSession,
  useInvalidateBillingDataAfterCredit,
} from "@multica/core/billing";
import type {
  BillingBatch,
  BillingPriceTier,
  BillingTopup,
  BillingTransaction,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { useLocale, useT } from "../i18n";
import { useNavigation } from "../navigation";

// 1 credit = 1_000_000 micro-credit；cents → 美元的换算因子用于 Stripe 侧展示
// 列。数值含义记录在 cloud billing.md 顶部，避免在 UI 里到处撒魔法数字。
const MICRO_PER_CREDIT = 1_000_000;
const CENTS_PER_DOLLAR = 100;

/** 计费测试页：集中展示余额、购买、Portal 与三类记录列表。 */
export function BillingTestPage() {
  const { t } = useT("billing");
  const { searchParams, replace, pathname } = useNavigation();

  // 云端 Stripe 成功 URL 带有字面的 {CHECKOUT_SESSION_ID} 占位符，Stripe 在
  // 重定向前会替换它。所以落到这里时，参数是真实存在的。
  const sessionId = searchParams.get("session_id") ?? "";

  return (
    <div className="space-y-6 p-6">
      <header>
        <h1 className="text-title-lg font-semibold">{t(($) => $.title)}</h1>
        <p className="mt-1 text-body text-muted-foreground">
          {t(($) => $.subtitle)}
        </p>
      </header>

      {sessionId && (
        <CheckoutSessionStatusBanner
          sessionId={sessionId}
          onDismiss={() => {
            // 展示完终态后把 session_id 从 URL 里去掉，这样刷新不会重新轮询
            // 一个过期订单。`replace` 让浏览器留在同一 pathname 而不新增历史。
            replace(pathname);
          }}
        />
      )}

      <BalanceCard />

      <BuyAndPortalSection />

      <TransactionsCard />

      <BatchesCard />

      <TopupsCard />
    </div>
  );
}

// ─── Stripe 成功横幅 ───────────────────────────────────────────────

// 每 2 秒轮询一次 /checkout-sessions/{id}，直到订单进入终态。仅在 URL 里有
// ?session_id 时挂载。
function CheckoutSessionStatusBanner({
  sessionId,
  onDismiss,
}: {
  sessionId: string;
  onDismiss: () => void;
}) {
  const { t } = useT("billing");
  const locale = useLocale();
  const { data, isLoading, isError, error } = useQuery(
    billingCheckoutSessionOptions(sessionId),
  );

  const status = data?.status ?? (isLoading ? "loading" : "");
  const terminal =
    status === "credited" || status === "failed" || status === "canceled";

  // 轮询到达终态时，页面其余部分（余额、交易、批次、充值）仍展示着 checkout
  // 前的旧快照。没有这个 effect，用户会看到上面写着「最终状态：credited」，
  // 余额卡却还是旧数字——唯一的过期信号只剩手动点刷新。失效这些依赖查询，
  // 让它们在后台重新拉取。
  //
  // 依赖列表 `terminal`：terminal 每个 session-id 只会从 false→true 翻转一次，
  // 所以失效恰好触发一次。若调用方带着一个已处终态的 session 挂载本横幅
  // （例如用户关闭再打开标签页后回访成功 URL），terminal 在首次数据加载时就
  // false→true，我们仍会重新拉取——这正是想要的，因为那种情况下缓存快照
  // 同样过期。
  const invalidateBillingDataAfterCredit = useInvalidateBillingDataAfterCredit();
  useEffect(() => {
    if (terminal) invalidateBillingDataAfterCredit();
  }, [terminal, invalidateBillingDataAfterCredit]);

  return (
    <Card className="border-primary/40 bg-primary/5">
      <CardHeader>
        <CardTitle className="text-body">
          {t(($) => $.checkout.session_label, { prefix: sessionId.slice(0, 16) })}
        </CardTitle>
        <CardDescription className="text-caption">
          {isLoading
            ? t(($) => $.checkout.loading)
            : isError
              ? t(($) => $.checkout.fetch_failed, {
                  error:
                    error instanceof Error
                      ? error.message
                      : t(($) => $.checkout.fetch_failed_unknown),
                })
              : terminal
                ? t(($) => $.checkout.final_status, { status })
                : t(($) => $.checkout.polling_status, {
                    status: status || t(($) => $.checkout.status_unknown),
                  })}
        </CardDescription>
      </CardHeader>
      {data && (
        <CardContent className="text-caption">
          <dl className="grid grid-cols-[120px_1fr] gap-y-1">
            <dt className="text-muted-foreground">{t(($) => $.checkout.label_order)}</dt>
            <dd className="font-mono">{data.order_id}</dd>
            <dt className="text-muted-foreground">{t(($) => $.checkout.label_tier)}</dt>
            <dd>{data.tier_id}</dd>
            <dt className="text-muted-foreground">{t(($) => $.checkout.label_charged)}</dt>
            <dd>
              {data.bonus_credits > 0
                ? t(($) => $.checkout.charged_with_bonus, {
                    money: formatMoney(data.amount_cents, data.currency, locale),
                    credits: data.credits.toLocaleString(locale),
                    bonus: data.bonus_credits.toLocaleString(locale),
                  })
                : t(($) => $.checkout.charged_value, {
                    money: formatMoney(data.amount_cents, data.currency, locale),
                    credits: data.credits.toLocaleString(locale),
                  })}
            </dd>
          </dl>
          {terminal && (
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={onDismiss}
            >
              {t(($) => $.checkout.clear_url)}
            </Button>
          )}
        </CardContent>
      )}
    </Card>
  );
}

// ─── 余额 ─────────────────────────────────────────────────────────

/** 余额卡片：展示当前 credit 余额与来源元信息。 */
function BalanceCard() {
  const { t } = useT("billing");
  const locale = useLocale();
  const balance = useQuery(billingBalanceOptions());

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <div>
          <CardTitle className="text-body">{t(($) => $.balance.title)}</CardTitle>
          <CardDescription className="text-caption">
            {t(($) => $.endpoints.balance)}
          </CardDescription>
        </div>
        <RefreshButton
          isLoading={balance.isFetching}
          onClick={() => void balance.refetch()}
        />
      </CardHeader>
      <CardContent>
        {balance.isLoading ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : balance.isError ? (
          <ErrorText error={balance.error} />
        ) : (
          <div className="space-y-1 text-body">
            <div className="text-display-sm font-semibold tabular-nums">
              {balance.data?.balance_credit.toLocaleString(locale) ?? 0}
              <span className="ml-1 text-body font-normal text-muted-foreground">
                {t(($) => $.balance.credits_suffix)}
              </span>
            </div>
            <div className="text-caption text-muted-foreground">
              {t(($) => $.balance.meta, {
                micro: balance.data?.balance_micro.toLocaleString(locale) ?? 0,
                owner: balance.data?.owner_id.slice(0, 8) ?? "",
                updated: formatDate(balance.data?.updated_at, t, locale),
              })}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ─── 购买 + Portal ─────────────────────────────────────────────────

/** 购买区：各档位购买按钮 + 打开 Billing Portal 的入口。 */
function BuyAndPortalSection() {
  const { t } = useT("billing");
  const tiers = useQuery(billingPriceTiersOptions());
  const createCheckout = useCreateCloudBillingCheckoutSession();
  const createPortal = useCreateCloudBillingPortalSession();
  const [busyTier, setBusyTier] = useState<string | null>(null);

  const handleBuy = async (tier: BillingPriceTier) => {
    setBusyTier(tier.id);
    try {
      const { url } = await createCheckout.mutateAsync({ tier_id: tier.id });
      if (!url) {
        toast.error(t(($) => $.buy.toast_no_url));
        return;
      }
      // 用 window.location 而不是 window.open 重定向，这样 Stripe 转出去后，
      // 浏览器返回键能把用户带回本页。Stripe 托管的页面会自己处理之后的
      // SPA 式行为。
      window.location.href = url;
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t(($) => $.buy.toast_checkout_failed),
      );
    } finally {
      setBusyTier(null);
    }
  };

  const handlePortal = async () => {
    try {
      const { url } = await createPortal.mutateAsync();
      if (!url) {
        toast.error(t(($) => $.buy.toast_no_portal_url));
        return;
      }
      // 新标签页打开——Portal 是客户自助界面，本页保留登录会话便于回来后
      // 用同一页验证结果状态。
      window.open(url, "_blank", "noopener,noreferrer");
    } catch (err) {
      // 400 是上游文档化的「还没有 Stripe 客户」场景。原样展示响应正文——
      // 测试期间这是最有用的信号。
      toast.error(
        err instanceof Error ? err.message : t(($) => $.buy.toast_portal_failed),
      );
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-body">{t(($) => $.buy.title)}</CardTitle>
        <CardDescription className="text-caption">
          {t(($) => $.endpoints.buy)}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {tiers.isLoading ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : tiers.isError ? (
          <ErrorText error={tiers.error} />
        ) : tiers.data?.length ? (
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {tiers.data.map((tier) => (
              <TierButton
                key={tier.id}
                tier={tier}
                busy={busyTier === tier.id}
                disabled={busyTier !== null}
                onClick={() => void handleBuy(tier)}
              />
            ))}
          </div>
        ) : (
          <p className="text-caption text-muted-foreground">{t(($) => $.buy.no_tiers)}</p>
        )}

        <div className="border-t pt-4">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={createPortal.isPending}
            onClick={() => void handlePortal()}
          >
            {createPortal.isPending ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
            )}
            {t(($) => $.buy.open_portal)}
          </Button>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.buy.portal_hint)}
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

/** 单个档位的购买按钮：金额 → 可得 credit 数（含可选赠送与有效期）。 */
function TierButton({
  tier,
  busy,
  disabled,
  onClick,
}: {
  tier: BillingPriceTier;
  busy: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  const { t } = useT("billing");
  const locale = useLocale();
  const display = tier.display_name || tier.id;
  const baseLine = t(($) => $.buy.tier_money_to_credits, {
    money: formatMoney(tier.amount_cents, "usd", locale),
    credits: tier.credits.toLocaleString(locale),
  });
  const bonusLine = tier.bonus_credits
    ? tier.bonus_expires_in
      ? t(($) => $.buy.tier_bonus_with_expiry, {
          credits: tier.bonus_credits.toLocaleString(locale),
          expiry: tier.bonus_expires_in,
        })
      : t(($) => $.buy.tier_bonus, {
          credits: tier.bonus_credits.toLocaleString(locale),
        })
    : "";
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className="rounded-md border bg-background p-3 text-left transition hover:border-primary disabled:cursor-not-allowed disabled:opacity-50"
    >
      <div className="flex items-center justify-between">
        <div className="text-body font-medium">{display}</div>
        {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
      </div>
      <div className="mt-1 text-caption text-muted-foreground">
        {baseLine}
        {bonusLine}
      </div>
      <div className="mt-1 font-mono text-micro text-muted-foreground">
        {t(($) => $.buy.tier_id, { id: tier.id })}
      </div>
    </button>
  );
}

// ─── 记录列表 ───────────────────────────────────────────────────────

/** 交易记录卡片：最近 20 条额度变动流水。 */
function TransactionsCard() {
  const { t } = useT("billing");
  const txs = useQuery(billingTransactionsOptions({ page: 1, page_size: 20 }));
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <div>
          <CardTitle className="text-body">{t(($) => $.transactions.title)}</CardTitle>
          <CardDescription className="text-caption">
            {t(($) => $.endpoints.transactions)}
          </CardDescription>
        </div>
        <RefreshButton
          isLoading={txs.isFetching}
          onClick={() => void txs.refetch()}
        />
      </CardHeader>
      <CardContent>
        {txs.isLoading ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : txs.isError ? (
          <ErrorText error={txs.error} />
        ) : txs.data?.items.length ? (
          <ul className="space-y-2 text-caption">
            {txs.data.items.map((row) => (
              <TransactionRow key={row.id} row={row} />
            ))}
          </ul>
        ) : (
          <EmptyText>{t(($) => $.transactions.empty)}</EmptyText>
        )}
        <PagingFooter
          page={txs.data?.page ?? 1}
          pageSize={txs.data?.page_size ?? 20}
          total={txs.data?.total ?? 0}
        />
      </CardContent>
    </Card>
  );
}

/** 单条交易记录行：类型、来源、增减额与变更后余额。 */
function TransactionRow({ row }: { row: BillingTransaction }) {
  const { t } = useT("billing");
  const locale = useLocale();
  const credit = row.amount_micro / MICRO_PER_CREDIT;
  return (
    <li className="rounded-md border bg-background p-2.5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-caption font-medium">
          {row.tx_type}
          <span className="ml-1.5 rounded bg-muted px-1.5 py-0.5 font-mono text-micro text-muted-foreground">
            {row.source}
          </span>
        </span>
        <span
          className={`text-body tabular-nums ${
            credit >= 0
              ? "text-green-700 dark:text-green-400"
              : "text-red-700 dark:text-red-400"
          }`}
        >
          {t(($) => $.transactions.credits_value, {
            value: `${credit >= 0 ? "+" : ""}${credit.toLocaleString(locale)}`,
          })}
        </span>
      </div>
      {row.description && (
        <div className="mt-1 text-caption text-muted-foreground">{row.description}</div>
      )}
      <div className="mt-1 font-mono text-micro text-muted-foreground">
        {t(($) => $.transactions.row_meta, {
          date: formatDate(row.created_at, t, locale),
          balance: (row.balance_after / MICRO_PER_CREDIT).toLocaleString(locale),
          ref: row.reference_id || t(($) => $.transactions.ref_empty),
        })}
      </div>
    </li>
  );
}

/** 批次（credit batch）卡片：各充值批次的总量 / 剩余量。 */
function BatchesCard() {
  const { t } = useT("billing");
  const batches = useQuery(billingBatchesOptions({ page: 1, page_size: 20 }));
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <div>
          <CardTitle className="text-body">{t(($) => $.batches.title)}</CardTitle>
          <CardDescription className="text-caption">
            {t(($) => $.endpoints.batches)}
          </CardDescription>
        </div>
        <RefreshButton
          isLoading={batches.isFetching}
          onClick={() => void batches.refetch()}
        />
      </CardHeader>
      <CardContent>
        {batches.isLoading ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : batches.isError ? (
          <ErrorText error={batches.error} />
        ) : batches.data?.items.length ? (
          <ul className="space-y-2 text-caption">
            {batches.data.items.map((row) => (
              <BatchRow key={row.id} row={row} />
            ))}
          </ul>
        ) : (
          <EmptyText>{t(($) => $.batches.empty)}</EmptyText>
        )}
        <PagingFooter
          page={batches.data?.page ?? 1}
          pageSize={batches.data?.page_size ?? 20}
          total={batches.data?.total ?? 0}
        />
      </CardContent>
    </Card>
  );
}

/** 单行批次：来源类型、已用 / 总量与过期时间。 */
function BatchRow({ row }: { row: BillingBatch }) {
  const { t } = useT("billing");
  const locale = useLocale();
  const total = row.total_micro / MICRO_PER_CREDIT;
  const remaining = row.remaining_micro / MICRO_PER_CREDIT;
  const consumed = total - remaining;
  return (
    <li className="rounded-md border bg-background p-2.5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-caption font-medium">
          {row.source_type}
          <span className="ml-1.5 font-mono text-micro text-muted-foreground">
            {t(($) => $.batches.id_suffix, { id: row.id.slice(0, 8) })}
          </span>
        </span>
        <span className="text-body tabular-nums">
          {t(($) => $.batches.remaining_over_total, {
            remaining: remaining.toLocaleString(locale),
            total: total.toLocaleString(locale),
          })}
        </span>
      </div>
      <div className="mt-1 text-caption text-muted-foreground">
        {t(($) => $.batches.consumed, { value: consumed.toLocaleString(locale) })}
        {row.expires_at
          ? t(($) => $.batches.expires_suffix, { value: formatDate(row.expires_at, t, locale) })
          : t(($) => $.batches.never_expires_suffix)}
      </div>
    </li>
  );
}

/** 充值（topup）卡片：历次 Stripe 充值记录。 */
function TopupsCard() {
  const { t } = useT("billing");
  const topups = useQuery(billingTopupsOptions({ page: 1, page_size: 20 }));
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <div>
          <CardTitle className="text-body">{t(($) => $.topups.title)}</CardTitle>
          <CardDescription className="text-caption">
            {t(($) => $.endpoints.topups)}
          </CardDescription>
        </div>
        <RefreshButton
          isLoading={topups.isFetching}
          onClick={() => void topups.refetch()}
        />
      </CardHeader>
      <CardContent>
        {topups.isLoading ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : topups.isError ? (
          <ErrorText error={topups.error} />
        ) : topups.data?.items.length ? (
          <ul className="space-y-2 text-caption">
            {topups.data.items.map((row) => (
              <TopupRow key={row.id} row={row} />
            ))}
          </ul>
        ) : (
          <EmptyText>{t(($) => $.topups.empty)}</EmptyText>
        )}
        <PagingFooter
          page={topups.data?.page ?? 1}
          pageSize={topups.data?.page_size ?? 20}
          total={topups.data?.total ?? 0}
        />
      </CardContent>
    </Card>
  );
}

/** 单行充值：金额 → credit（含赠送）与状态徽标。 */
function TopupRow({ row }: { row: BillingTopup }) {
  const { t } = useT("billing");
  const locale = useLocale();
  return (
    <li className="rounded-md border bg-background p-2.5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-caption font-medium">
          {row.tier_id || row.id.slice(0, 8)}
          <span
            className={`ml-1.5 rounded px-1.5 py-0.5 font-mono text-micro ${
              row.status === "credited"
                ? "bg-green-500/10 text-green-700 dark:text-green-400"
                : row.status === "failed" || row.status === "canceled"
                  ? "bg-red-500/10 text-red-700 dark:text-red-400"
                  : "bg-amber-500/10 text-amber-700 dark:text-amber-400"
            }`}
          >
            {row.status}
          </span>
        </span>
        <span className="text-body tabular-nums">
          {row.bonus_credits > 0
            ? t(($) => $.topups.amount_to_credits_with_bonus, {
                money: formatMoney(row.amount_cents, row.currency, locale),
                credits: row.credits.toLocaleString(locale),
                bonus: row.bonus_credits.toLocaleString(locale),
              })
            : t(($) => $.topups.amount_to_credits, {
                money: formatMoney(row.amount_cents, row.currency, locale),
                credits: row.credits.toLocaleString(locale),
              })}
        </span>
      </div>
      <div className="mt-1 font-mono text-micro text-muted-foreground">
        {t(($) => $.topups.row_meta, {
          date: formatDate(row.created_at, t, locale),
          checkout: row.stripe_checkout_id || t(($) => $.topups.stripe_empty),
        })}
      </div>
    </li>
  );
}

// ─── 共用小件 ─────────────────────────────────────────────────────

/** 列表底部的分页摘要文案。 */
function PagingFooter({
  page,
  pageSize,
  total,
}: {
  page: number;
  pageSize: number;
  total: number;
}) {
  const { t } = useT("billing");
  if (total === 0) return null;
  return (
    <div className="mt-3 text-micro text-muted-foreground">
      {t(($) => $.shared.paging, {
        page,
        totalPages: Math.max(1, Math.ceil(total / pageSize)),
        total,
      })}
    </div>
  );
}

/** 卡片右上角的手动刷新按钮（加载时转圈并禁用）。 */
function RefreshButton({
  isLoading,
  onClick,
}: {
  isLoading: boolean;
  onClick: () => void;
}) {
  const { t } = useT("billing");
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="h-7 w-7 p-0"
      onClick={onClick}
      disabled={isLoading}
      aria-label={t(($) => $.shared.refresh)}
    >
      <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? "animate-spin" : ""}`} />
    </Button>
  );
}

/** 红色的错误提示文本。 */
function ErrorText({ error }: { error: unknown }) {
  const { t } = useT("billing");
  return (
    <p className="text-caption text-destructive">
      {error instanceof Error ? error.message : t(($) => $.shared.request_failed)}
    </p>
  );
}

/** 列表为空时的灰字提示。 */
function EmptyText({ children }: { children: React.ReactNode }) {
  return <p className="text-caption text-muted-foreground">{children}</p>;
}

/**
 * 把以分为单位的金额格式化成带货币符号的本地化文本。
 * 这里用 Intl 没问题——没有货币换算，只是用周围翻译句子的同一 locale 做
 * 规范化展示。
 */
function formatMoney(
  amountCents: number,
  currency: string,
  locale: string,
): string {
  try {
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency: currency.toUpperCase(),
    }).format(amountCents / CENTS_PER_DOLLAR);
  } catch {
    return `${(amountCents / CENTS_PER_DOLLAR).toFixed(2)} ${currency.toUpperCase()}`;
  }
}

// formatDate 返回 ISO 风格本地化字符串，缺失/非法输入时返回 locale 的
// "—" 破折号。破折号本身也走翻译包，这样若任一 locale 想换成本地化的占位符
// 也能保持一致。由调用方传入 `t`，避免在 util 里调用 useT()（hooks 规则会
// 被这里的条件路径绊倒）。
function formatDate(
  value: string | undefined,
  t: ReturnType<typeof useT<"billing">>["t"],
  locale: string,
): string {
  if (!value) return t(($) => $.shared.date_dash);
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString(locale);
}
