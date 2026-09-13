/** 计费页路由：渲染云端计费 API 的测试页。 */
import { BillingTestPage } from "@multica/views/billing";

// 账号级的云端计费测试页。虽然它放在 [workspaceSlug] 下——这是 dashboard 布局
// 对每个页面的位置要求——但这里的数据都不按 workspace 隔离。slug 只是让该路由
// 落在已登录的外壳之内。
export default function BillingRoute() {
  return <BillingTestPage />;
}
