import { BillingReturnPage } from "@multica/views/billing";

/**
 * `/billing/return` —— Stripe Checkout 和 Billing Portal 把浏览器送回的地方。
 *
 * 故意做成顶层路由而不是 workspace 作用域下的：部署在云端只配置一组 return URL，
 * 所以它们没法携带 workspace slug。云端会带上它授权过的 workspace，
 * 本页把它解析成 slug 后再转发到「工作区设置 → 计费」。`billing` 本身已经是保留 slug，
 * 所以不会遮蔽真实 workspace。
 *
 * 桌面端在这里没有自己的路由：Stripe 在系统浏览器里打开，所以 return 总是落到 Web 应用上。
 * 桌面端的 Billing 标签页在窗口重新获得焦点时会读取新状态。
 *
 * @returns 计费返回页。
 */
export default function BillingReturnRoute() {
  return <BillingReturnPage />;
}
