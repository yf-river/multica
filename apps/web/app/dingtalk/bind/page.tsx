"use client";

/**
 * 钉钉机器人账号绑定页：从 `?token=` 读取绑定凭据，渲染共享的 <DingTalkBindPage />。
 */
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { DingTalkBindPage } from "@multica/views/dingtalk";

// /dingtalk/bind?token=<raw> 是机器人「关联你的账号」的落点。
// 按 Next.js 15 的 CSR-bailout 规则，用 Suspense 包裹 useSearchParams；加载文案实际上不会画出来，
// 因为兑换页自己会立刻渲染「兑换中…」状态。
function DingTalkBindPageContent() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  return <DingTalkBindPage token={token} />;
}

/**
 * 绑定页默认导出：用 Suspense 包裹内容组件。
 *
 * @returns 钉钉绑定页。
 */
export default function Page() {
  return (
    <Suspense fallback={null}>
      <DingTalkBindPageContent />
    </Suspense>
  );
}
