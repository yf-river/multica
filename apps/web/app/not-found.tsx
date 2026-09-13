"use client";

import Link from "next/link";
import { buttonVariants } from "@multica/ui/components/ui/button";

/**
 * 404 页面：路由未匹配到时展示的兜底界面。
 *
 * @returns 居中的「页面不存在」提示，附带返回首页的链接。
 */
export default function NotFound() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-6 px-6 py-24 text-center">
      <p className="text-body font-medium text-muted-foreground">404</p>
      <h1 className="text-display-sm font-semibold tracking-tight">页面不存在</h1>
      <p className="max-w-md text-body text-muted-foreground">
        The page you are looking for doesn&rsquo;t exist or has been moved.
      </p>
      <Link href="/" className={buttonVariants({ className: "mt-2" })}>
        Back to Multica
      </Link>
    </main>
  );
}
