/** 官方营销域名判断：用于区分「营销站」与「应用/自托管」两种部署形态。 */

/** 官方营销域名集合（根域名与 www 都算）。 */
const OFFICIAL_MARKETING_HOSTS = new Set(["multica.ai", "www.multica.ai"]);

/**
 * 判断当前 hostname 是否官方营销域名（大小写、首尾点号都做了归一化）。
 *
 * @param hostname - 浏览器 location.hostname。
 * @returns 是否为官方营销域名。
 */
export function isOfficialMarketingHost(hostname: string): boolean {
  const normalized = hostname.trim().toLowerCase().replace(/\.$/, "");
  return OFFICIAL_MARKETING_HOSTS.has(normalized);
}
