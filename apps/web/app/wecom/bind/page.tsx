"use client";

/**
 * 企业微信智能机器人账号绑定页：从 `?token=` 读取绑定凭据，渲染共享的 <WecomBindPage />。
 */
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { WecomBindPage } from "@multica/views/wecom";

// /wecom/bind?token=<raw> 是智能机器人「关联你的 Multica 账号」的落点。
// 按 Next.js 15 的 CSR-bailout 规则用 Suspense 包裹 useSearchParams；加载文案实际上不会画出来，
// 因为兑换页自己会立刻渲染「兑换中…」状态。
function WecomBindPageContent() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  return <WecomBindPage token={token} />;
}

/**
 * 绑定页默认导出：用 Suspense 包裹内容组件。
 *
 * @returns 企业微信绑定页。
 */
export default function Page() {
  return (
    <Suspense fallback={null}>
      <WecomBindPageContent />
    </Suspense>
  );
}
