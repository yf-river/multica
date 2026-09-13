// 本文件提供一组纯函数，用于处理「购买席位」和「邀请成员」之间的各种状态判定。

import type {
  PurchaseWorkspaceSeatsResponse,
  WorkspaceSeatPurchasePreview,
  WorkspaceSubscriptionSummary,
} from "@multica/core/types";

/**
 * 席位容量导致的邀请失败类型：
 * - full：席位已满
 * - unavailable：席位信息暂不可用
 * - rate_limited：触发限流
 */
export type SeatInvitationCapacityFailure =
  | "full"
  | "unavailable"
  | "rate_limited";

/**
 * 把服务端返回的错误码映射成更易读的席位容量失败类型。
 * @param errorCode - 服务端错误码。
 * @returns 对应的失败类型；不认识该错误码时返回 undefined。
 */
export function seatInvitationCapacityFailure(
  errorCode: string | undefined,
): SeatInvitationCapacityFailure | undefined {
  switch (errorCode) {
    case "seat_capacity_full":
      return "full";
    case "seat_capacity_unavailable":
      return "unavailable";
    case "seat_capacity_rate_limited":
      return "rate_limited";
    default:
      return undefined;
  }
}

/**
 * 判断邀请失败后，是否可以在「购买席位」之后重试。
 * 只有「席位不可用」或「被限流」这两种情况适合购买后重试，席位已满则不行。
 * @param errorCode - 服务端错误码。
 * @returns 是否支持购买后重试。
 */
export function seatInvitationCanRetryAfterPurchase(
  errorCode: string | undefined,
): boolean {
  const failure = seatInvitationCapacityFailure(errorCode);
  return failure === "unavailable" || failure === "rate_limited";
}

/**
 * 判断一个购买预览是否是「只加购一个席位」的简单场景。
 * 额外要求各金额字段都是合法的安全整数，确保后续金额展示不会出错。
 * @param preview - 购买预览数据，可能为空。
 * @returns 类型守卫：true 时表明这是单席位邀请预览。
 */
export function isSingleSeatInvitePreview(
  preview: WorkspaceSeatPurchasePreview | null | undefined,
): preview is WorkspaceSeatPurchasePreview {
  return Boolean(
    preview &&
      preview.additionalSeats === 1 &&
      preview.resultingSeats === preview.currentSeats + 1 &&
      Number.isSafeInteger(preview.prorationAmount) &&
      preview.prorationAmount >= 0 &&
      Number.isSafeInteger(preview.nextInvoiceAmount) &&
      preview.nextInvoiceAmount >= 0,
  );
}

/**
 * 校验购买成功后的响应是否与之前展示给用户的预览一致（席位数量、货币都相同）。
 * 用于确认用户实际买到的东西和下单前看到的一致，避免展示错乱。
 * @param response - 购买接口返回结果，可能为空。
 * @param preview - 下单前的购买预览。
 * @returns 类型守卫：true 时表明响应与预览匹配。
 */
export function seatPurchaseMatchesPreview(
  response: PurchaseWorkspaceSeatsResponse | null | undefined,
  preview: WorkspaceSeatPurchasePreview,
): response is PurchaseWorkspaceSeatsResponse {
  return Boolean(
    response &&
      response.currentSeats === preview.currentSeats &&
      response.additionalSeats === preview.additionalSeats &&
      response.resultingSeats === preview.resultingSeats &&
      response.currency === preview.currency,
  );
}

/**
 * 判断购买失败后，能否直接用同一份报价再次重试。
 * 某些错误（如支付失败、报价已变更、容量已变更）表示报价已失效，必须重新报价；
 * 其他错误通常可以沿用原报价重试。
 * @param errorCode - 服务端错误码。
 * @returns 是否可用同一份报价重试。
 */
export function seatPurchaseCanRetryWithSameQuote(
  errorCode: string | undefined,
): boolean {
  return ![
    "seat_purchase_payment_failed",
    "seat_purchase_in_progress",
    "seat_quote_changed",
    "seat_capacity_changed",
  ].includes(errorCode ?? "");
}

/**
 * 判断刚购买的席位是否已经「到账」、可以开始邀请成员。
 * 要求订阅摘要的更新时间晚于下单时间（说明服务器已处理完购买），
 * 且已购买席位数达到预期、且仍有可用席位。
 * @param summary - 最新订阅摘要。
 * @param preview - 下单时的购买预览。
 * @param submittedAt - 下单提交时间戳。
 * @param summaryUpdatedAt - 订阅摘要最后更新时间戳。
 * @returns 是否已就绪、可以发起邀请。
 */
export function purchasedSeatIsReadyForInvitation(
  summary: WorkspaceSubscriptionSummary | null | undefined,
  preview: WorkspaceSeatPurchasePreview,
  submittedAt: number,
  summaryUpdatedAt: number,
): boolean {
  const capacity = summary?.seatCapacity;
  return Boolean(
    summaryUpdatedAt >= submittedAt &&
      capacity &&
      capacity.purchased >= preview.resultingSeats &&
      capacity.available >= 1,
  );
}
