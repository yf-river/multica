"use client";

/**
 * dingtalk（钉钉）集成模块：绑定凭据兑换落地页。
 * 机器人的「关联你的账号」提示会指向这里。用户落地时可能已登录也可能未登录；
 * 兑换前必须登录，因为兑换者的 Multica 身份取自会话（token 本身从来证明不了
 * 是谁在绑定）。
 *
 * token 经 `?token=<raw>` 传入。把它 POST 给 /api/dingtalk/binding/redeem；后端
 * 返回 410（无效/过期）、409（已绑到别的用户）、403（不是工作区成员）或 200
 * 及已绑定的安装。每种结果经 common.json 里的 dingtalk_bind 文案映射成不同
 * 的提示。
 */

import { useEffect, useRef, useState } from "react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Button } from "@multica/ui/components/ui/button";
import { api, ApiError } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { AppLink } from "../navigation";
import { useT } from "../i18n";

type RedeemState =
  | { kind: "idle" }
  | { kind: "redeeming" }
  | { kind: "done"; workspaceId: string; installationId: string }
  | { kind: "needs-auth" }
  | { kind: "error"; reason: string };

/** 钉钉绑定页主体：兑换 token 并按结果展示相应状态。 */
export function DingTalkBindPage({ token }: { token: string | null }) {
  const { t } = useT("common");
  const user = useAuthStore((s) => s.user);
  const isAuthLoading = useAuthStore((s) => s.isLoading);
  const [state, setState] = useState<RedeemState>({ kind: "idle" });
  const redeemingToken = useRef<string | null>(null);

  useEffect(() => {
    if (!token) {
      setState({ kind: "error", reason: "missing_token" });
      return;
    }
    if (isAuthLoading) return;
    if (!user) {
      setState({ kind: "needs-auth" });
      return;
    }
    if (state.kind !== "idle" && state.kind !== "needs-auth") return;
    if (redeemingToken.current === token) return;
    redeemingToken.current = token;
    setState({ kind: "redeeming" });
    (async () => {
      try {
        const resp = await api.redeemDingTalkBindingToken(token);
        setState({
          kind: "done",
          workspaceId: resp.workspace_id,
          installationId: resp.installation_id,
        });
      } catch (e) {
        setState({
          kind: "error",
          reason: redemptionFailureReason(e),
        });
      }
    })();
  }, [token, user, isAuthLoading, state.kind]);

  return (
    <div className="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center p-6">
      <Card className="w-full">
        <CardContent className="space-y-4">
          <h1 className="text-title font-semibold">{t(($) => $.dingtalk_bind.page_title)}</h1>
          {state.kind === "idle" || state.kind === "redeeming" ? (
            <p className="text-body text-muted-foreground">{t(($) => $.dingtalk_bind.redeeming)}</p>
          ) : state.kind === "needs-auth" ? (
            <>
              <p className="text-body text-muted-foreground">
                {t(($) => $.dingtalk_bind.needs_auth_description)}
              </p>
              <Button
                size="sm"
                render={
                  <AppLink
                    href={`/login?next=${encodeURIComponent(
                      `/dingtalk/bind?token=${encodeURIComponent(token ?? "")}`,
                    )}`}
                  />
                }
                nativeButton={false}
              >
                {t(($) => $.dingtalk_bind.sign_in)}
              </Button>
            </>
          ) : state.kind === "done" ? (
            <>
              <p className="text-body font-medium">{t(($) => $.dingtalk_bind.done_title)}</p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.dingtalk_bind.done_description)}
              </p>
            </>
          ) : (
            <>
              <p className="text-body font-medium">{t(($) => $.dingtalk_bind.error_title)}</p>
              <p className="text-caption text-muted-foreground">
                {(() => {
                  switch (state.reason) {
                    case "missing_token":
                      return t(($) => $.dingtalk_bind.error_missing_token);
                    case "expired":
                      return t(($) => $.dingtalk_bind.error_expired);
                    case "already_bound":
                      return t(($) => $.dingtalk_bind.error_already_bound);
                    case "not_member":
                      return t(($) => $.dingtalk_bind.error_not_member);
                    default:
                      return t(($) => $.dingtalk_bind.error_unknown);
                  }
                })()}
              </p>
              <p className="text-micro text-muted-foreground">
                {t(($) => $.dingtalk_bind.error_admin_hint)}
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// 按服务端 HTTP 状态码而不是错误文本来归类兑换失败：后端返回 410
// （无效/过期）、409（已绑到别的用户）或 403（不是工作区成员）。用
// ApiError.status 匹配，让文案分支与服务端的具体措辞解耦。
function redemptionFailureReason(err: unknown): string {
  if (!(err instanceof ApiError)) return "unknown";
  switch (err.status) {
    case 410:
      return "expired";
    case 409:
      return "already_bound";
    case 403:
      return "not_member";
    default:
      return "unknown";
  }
}
