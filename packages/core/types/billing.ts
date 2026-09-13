// 本文件镜像 multica-cloud 计费模块的响应形状（multica-cloud/docs/api/billing.md）。
// 这些类型是前端经 /api/cloud-billing/* 消费的契约——multica-api 自己不拥有
// 该 schema，只是字节转发。字段名要与云端发送的原样一致。
//
// 单位约定（摘自云文档）：
//   - micro-credit（BIGINT）：内部存储单位；1 credit = 1_000_000 micro
//   - credit：用户展示单位；1 USD = 1000 credit
// 展示给用户时始终优先 `*_credit` 字段；计算只用 `*_micro`，避免浮点漂移。

// GET /balance
/** 账号钱包余额。 */
export interface BillingBalance {
  owner_id: string;
  balance_micro: number;
  balance_credit: number;
  updated_at: string;
}

// `tx_type` 取值对应云文档枚举。导出为联合供参考/展示 switch；实际接口字段
// 下面仍类型为纯 `string`，这样云端未来扩展枚举不会弄崩解析器。前端遇到
// 已知值做 switch，未知值回退到通用展示。
export type BillingTxType =
  | "topup"
  | "deduction"
  | "refund"
  | "expire"
  | "adjustment";

// `source` 取值对应云文档枚举。放宽理由同 BillingTxType 上面的注释。
export type BillingTxSource =
  | "gateway"
  | "fleet"
  | "topup"
  | "refund"
  | "admin"
  | "system";

/** 一条钱包流水（余额变动记录）。 */
export interface BillingTransaction {
  id: string;
  owner_id: string;
  /** 幂等键——同一笔操作不会产生两条流水。 */
  idempotency_key: string;
  // tx_type / source 在这里放宽为 string，尽管云文档枚举了固定集合；
  // 见 BillingTxType 上的注释。关心的 UI 应对值 switch 并优雅兜底。
  tx_type: string;
  source: string;
  // amount_micro 正值代表入账，负值代表扣减。
  amount_micro: number;
  /** 该笔入账/扣减发生后的余额。 */
  balance_after: number;
  /** 关联的外部对象 id（如充值订单、扣费批次）。 */
  reference_id: string;
  description: string;
  metadata: Record<string, unknown>;
  created_at: string;
}

/** 流水分页响应。 */
export interface BillingTransactionsPage {
  items: BillingTransaction[];
  total: number;
  page: number;
  page_size: number;
}

// 一个信用批次（credit batch）的 `source_type`。`purchase` = 付费充值，
// `bonus` = 订阅/活动赠礼，`adjustment` = 运维修正。
// 导出为联合用于文档；字段在 BillingBatch 里放宽为 string（理由同
// BillingTxType）。
export type BillingBatchSourceType = "purchase" | "bonus" | "adjustment";

/** 一个信用批次（一笔带来源、可能带过期时间的信用包）。 */
export interface BillingBatch {
  id: string;
  owner_id: string;
  /** 产生本批次的源流水 id。 */
  source_tx_id: string;
  // 放宽为 string；见上方 BillingBatchSourceType 的注释。
  source_type: string;
  /** 批次总 micro 值。 */
  total_micro: number;
  /** 尚未消耗的剩余 micro 值。 */
  remaining_micro: number;
  // 省略 expires_at 表示该批次永不过期。赠礼批次通常带有效期；购买批次通常
  // 不带。
  expires_at?: string | null;
  created_at: string;
  updated_at: string;
}

/** 信用批次分页响应。 */
export interface BillingBatchesPage {
  items: BillingBatch[];
  total: number;
  page: number;
  page_size: number;
}

// 充值订单生命周期。`pending` = checkout 打开，`paid` = Stripe 已确认付款但
// 信用尚未入账，`credited` = 钱包已更新，`failed`/`canceled` = 终态失败。
// 导出为联合用于文档；字段在 BillingTopup / BillingCheckoutSessionStatus 里
// 放宽为 string。
export type BillingTopupStatus =
  | "pending"
  | "paid"
  | "credited"
  | "failed"
  | "canceled";

/** 一次充值订单（Stripe Checkout 对应的内部订单）。 */
export interface BillingTopup {
  id: string;
  owner_id: string;
  /** 支付金额（美分）。 */
  amount_cents: number;
  currency: string;
  /** 到账信用数。 */
  credits: number;
  /** 附赠信用数。 */
  bonus_credits: number;
  // 放宽为 string；见 BillingTopupStatus 注释。
  status: string;
  /** 购买的档位 id。 */
  tier_id: string;
  /** Stripe Checkout 会话 id。 */
  stripe_checkout_id: string;
  // 当 status 达到 `credited` 且购买批次已铸造时设置；在此之前为空。
  purchase_batch_id?: string;
  created_at: string;
  updated_at: string;
}

/** 充值订单分页响应。 */
export interface BillingTopupsPage {
  items: BillingTopup[];
  total: number;
  page: number;
  page_size: number;
}

// GET /price-tiers —— 返回服务器权威的可购买档位。
// 前端绝不硬编码 amount/credits；上游才是事实来源，价格可在不发前端版本
// 的情况下更新。
export interface BillingPriceTier {
  id: string;
  display_name: string;
  amount_cents: number;
  credits: number;
  // 两个 bonus 字段都可选：省略时不赠送。省略 `bonus_expires_in` 但存在
  // `bonus_credits` 时，赠送的信用永不过期。
  bonus_credits?: number;
  // Go time.Duration 字符串，如 "720h0m0s" = 30 天。
  bonus_expires_in?: string;
}

/** 创建 Stripe Checkout 会话的请求体（发起充值）。 */
export interface CreateBillingCheckoutSessionRequest {
  tier_id: string;
  // 可选的调用方提供的 email；仅当该用户还没有 Stripe customer 记录时被
  // Stripe Checkout 使用。已知用户邮箱并希望预填时传入。
  customer_email?: string;
}

/** 创建 Checkout 会话的响应：拿到重定向 URL 即完成。 */
export interface CreateBillingCheckoutSessionResponse {
  order_id: string;
  session_id: string;
  // Stripe 托管的 Checkout URL。前端把浏览器重定向到这里。
  url: string;
}

// GET /checkout-sessions/{session_id} —— 前端从 Stripe 返回（URL 带
// `?session_id=...`）后轮询此接口，在用户离开前展示信用状态。
export interface BillingCheckoutSessionStatus {
  order_id: string;
  // 放宽为 string；见 BillingTopupStatus 注释。
  status: string;
  amount_cents: number;
  credits: number;
  bonus_credits: number;
  currency: string;
  tier_id: string;
}

/** 创建 Stripe 门户会话（管理订阅/账单入口）的响应。 */
export interface CreateBillingPortalSessionResponse {
  url: string;
}

// ---------------------------------------------------------------------------
// Workspace 订阅（`/api/cloud-subscriptions/*`）
//
// 与上面账号级钱包类型不同，这里是 workspace 级。服务器从已鉴权的请求上下文
// 注入 workspace。客户端从不指定 workspace、金额或 Price ID。座位购买用加性
// 计数；Cloud 负责校验并换算成 Stripe 的绝对数量。
//
// 服务器可能不知道的每个字段都做可空而非默认。缺失值必须保持「可见地未知」：
// 把缺失的方案渲染成 Free，会把一次上游故障变成付费 workspace 的静默降级。

/** 订阅账单周期：按月或按年。 */
export type WorkspaceSubscriptionInterval = "month" | "year";

/** 某项额度的限制形态：有限额（给出上限）或无限（limit 为 null）。 */
export type WorkspaceEntitlementLimit =
  | { mode: "limited"; limit: number }
  | { mode: "unlimited"; limit: null };

/** WorkspaceEntitlementLimit 的 mode 取值（"limited" | "unlimited"）。 */
export type WorkspaceEntitlementLimitMode =
  WorkspaceEntitlementLimit["mode"];

/** 订阅方案对 workspace 的权益快照（能开多少 issue、跑多少 autopilot）。 */
export interface WorkspaceSubscriptionEntitlements {
  workspaceId: string;
  // 刻意开放字符串：云可能在不发客户端版本的情况下新增方案与 Stripe 状态，
  // 未知值必须读作「未知」而不是「Free」。
  plan: string;
  status: string;
  seats: number;
  limits: {
    /** 同时存在 issue 数的额度。 */
    issueCount: WorkspaceEntitlementLimit;
    /** autopilot 运行量的额度。 */
    autopilotRuns: WorkspaceEntitlementLimit;
  };
  currentPeriodEnd: string | null;
  snapshotExpiresAt: string | null;
  version: number;
}

/**
 * Workspace 计费设置页所需的、关于当前订阅的一切，来自 cloud 自身数据库——
 * 绝不是同步 Stripe 调用，因此在 Stripe 降级时它仍可读。
 */
export interface WorkspaceSubscriptionSummary {
  entitlement: WorkspaceSubscriptionEntitlements;
  billingInterval: WorkspaceSubscriptionInterval | null;
  /** 权威的当前人类成员数。agent 从不占座位。 */
  humanMembers: number;
  /** workspace 当前没有生效的已购容量时为 null。 */
  seatCapacity: WorkspaceSeatCapacity | null;
  cancelAtPeriodEnd: boolean;
  graceUntil: string | null;
  /** 是否存在本地 Stripe customer；绝不把它用作操作门控。 */
  hasStripeCustomer: boolean;
  /** cloud 针对「这位确切调用者 + 生效快照」授权的动作。 */
  availableActions: {
    checkout: boolean;
    portal: boolean;
    purchaseSeats: boolean;
  };
}

/** 议题额度的用量统计。 */
export interface IssueLimitUsage {
  used: number;
  limit: number;
}

/** 座位容量信息：已购多少、占用多少、还可用多少。 */
export interface WorkspaceSeatCapacity {
  purchased: number;
  used: number;
  reserved: number;
  /** 服务器计算的当前未用/未预留的已购容量。 */
  available: number;
  /** cloud 计算的人类座位超售状态。 */
  overcommitted: boolean;
  version: number;
  /** 若下一周期已安排了更低的购买量，则为其数值；否则为 null。 */
  pendingQuantity: number | null;
  activePurchase: WorkspaceSeatPurchaseSummary | null;
}

/** 一次进行中的座位购买概要。 */
export interface WorkspaceSeatPurchaseSummary {
  requestId: string;
  targetSeats: number;
  /** pending（排队）→ processing（处理中）→ submitted（已提交 Stripe）。 */
  status: "pending" | "processing" | "submitted";
  expiresAt: string | null;
}

/** 已配置 Stripe Price 的「展示安全」子集。id 留在服务端。 */
export interface WorkspaceSubscriptionPrice {
  currency: string;
  /** 每个许可座位的最小货币单位，如 2000 代表 $20.00。 */
  unitAmount: number;
  interval: WorkspaceSubscriptionInterval;
  intervalCount: number;
}

/** 月付/年付两档价格集合。 */
export interface WorkspaceSubscriptionPrices {
  month: WorkspaceSubscriptionPrice;
  year: WorkspaceSubscriptionPrice;
}

/** 发起订阅 Checkout 的请求体。 */
export interface CreateWorkspaceSubscriptionCheckoutRequest {
  interval: WorkspaceSubscriptionInterval;
  idempotencyKey: string;
}

/** 发起订阅 Checkout 的响应。 */
export interface CreateWorkspaceSubscriptionCheckoutResponse {
  requestId: string;
  sessionId: string;
  url: string;
}

/** 预览座位购买的请求体（加购数量）。 */
export interface PreviewWorkspaceSeatPurchaseRequest {
  additionalSeats: number;
}

/** 座位购买预览：加购前后的座位数与预计费用。 */
export interface WorkspaceSeatPurchasePreview {
  currentSeats: number;
  additionalSeats: number;
  resultingSeats: number;
  purchaseVersion: number;
  currency: string;
  /** Stripe 最小货币单位的预估即时按比例折算金额。 */
  prorationAmount: number;
  /** 下一期完整循环发票的预估金额（最小货币单位）。 */
  nextInvoiceAmount: number;
  quotedAt: string;
}

/** 确认购买座位的请求体（含乐观并发校验字段）。 */
export interface PurchaseWorkspaceSeatsRequest {
  /** 加购数量。 */
  additionalSeats: number;
  /** 期望的当前座位数——与实际不符时服务器拒绝（防并发覆盖）。 */
  expectedCurrentSeats: number;
  expectedPurchaseVersion: number;
  /** 用户认可的那笔按比例折算金额。 */
  acceptedProrationAmount: number;
  currency: string;
  idempotencyKey: string;
}

/** 确认购买座位的响应。 */
export interface PurchaseWorkspaceSeatsResponse {
  requestId: string;
  currentSeats: number;
  additionalSeats: number;
  resultingSeats: number;
  currency: string;
  prorationAmount: number;
  nextInvoiceAmount: number;
  /** pending → submitted → confirmed。 */
  status: "pending" | "submitted" | "confirmed";
}

/** 座位对账（把本地座位数与 cloud 实际一致化）的结果。 */
export interface WorkspaceSubscriptionSeatReconcileResult {
  workspaceId: string;
  /** 服务器执行的对账动作描述。 */
  action: string;
  version: number;
}

/** 创建订阅管理门户会话（管理/取消订阅）的响应。 */
export interface CreateWorkspaceSubscriptionPortalResponse {
  url: string;
}
