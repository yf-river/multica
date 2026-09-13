"use client";

/**
 * 飞书 Bot 账号绑定页：从 `?token=` 读取绑定凭据，渲染共享的 <LarkBindPage />。
 */
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { LarkBindPage } from "@multica/views/lark";

// /lark/bind?token=<raw> 是 Bot 的「你还没绑定，点这里」落点。
// 按 Next.js 15 的 CSR-bailout 规则用 Suspense 包裹 useSearchParams；加载文案实际上不会画出来，
// 因为兑换页自己会立刻渲染「兑换中…」状态。
function LarkBindPageContent() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  return <LarkBindPage token={token} />;
}

/**
 * 绑定页默认导出：用 Suspense 包裹内容组件。
 *
 * @returns 飞书绑定页。
 */
export default function Page() {
  return (
    <Suspense fallback={null}>
      <LarkBindPageContent />
    </Suspense>
  );
}
