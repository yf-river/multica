/** Dashboard 段路由切换时的 Suspense 加载骨架屏。 */
import { Skeleton } from "@multica/ui/components/ui/skeleton";

// 由 Next.js 在 (dashboard) 段内路由切换时作为 Suspense fallback 渲染。
// 仅作用于此段——登录/auth/落地页各自有全屏的加载态。
export default function DashboardLoading() {
  return (
    <div className="flex h-svh w-full flex-col">
      <div className="flex h-12 shrink-0 items-center gap-3 border-b px-4">
        <Skeleton className="h-5 w-5 rounded-md" />
        <Skeleton className="h-4 w-32" />
      </div>
      <div className="flex-1 space-y-2 p-4">
        {Array.from({ length: 8 }).map((_, i) => (
          <Skeleton key={i} className="h-9 w-full" />
        ))}
      </div>
    </div>
  );
}
