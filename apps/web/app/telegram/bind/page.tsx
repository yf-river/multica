"use client";

/**
 * Telegram 机器人账号绑定页：从 `?token=` 读取绑定凭据，渲染共享的 <TelegramBindPage />。
 */
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { TelegramBindPage } from "@multica/views/telegram";

// /telegram/bind?token=<raw> 是机器人「关联你的账号」的落点。
// 按 Next.js 15 的 CSR-bailout 规则用 Suspense 包裹 useSearchParams。
function TelegramBindPageContent() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  return <TelegramBindPage token={token} />;
}

/**
 * 绑定页默认导出：用 Suspense 包裹内容组件。
 *
 * @returns Telegram 绑定页。
 */
export default function Page() {
  return (
    <Suspense fallback={null}>
      <TelegramBindPageContent />
    </Suspense>
  );
}
