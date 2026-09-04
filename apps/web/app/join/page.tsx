"use client";

import { useEffect, useState, Suspense } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { api, errorCode } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Badge } from "@multica/ui/components/ui/badge";
import { useAuthStore } from "@multica/core/auth";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type { ShareLinkInfo, Workspace } from "@multica/core/types";

function JoinInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const code = searchParams.get("code");
  const user = useAuthStore((s) => s.user);
  const queryClient = useQueryClient();

  const [info, setInfo] = useState<ShareLinkInfo | null>(null);
  const [infoError, setInfoError] = useState<string | null>(null);
  const [joining, setJoining] = useState(false);
  const [joinError, setJoinError] = useState<string | null>(null);
  const [joined, setJoined] = useState(false);

  // Load the share-link preview once. No auth needed: this endpoint only
  // exposes the workspace name/slug and the inviter.
  useEffect(() => {
    if (!code) {
      setInfoError("未找到邀请码，请使用有效的邀请链接。");
      return;
    }
    let cancelled = false;
    api
      .getShareLinkInfo(code)
      .then((data) => {
        if (!cancelled) setInfo(data);
      })
      .catch(() => {
        if (!cancelled) setInfoError("邀请链接无效或已过期。");
      });
    return () => {
      cancelled = true;
    };
  }, [code]);

  const handleJoin = () => {
    if (!code) return;
    if (!user) {
      // Not logged in: send them to login, then return here to join.
      router.push(`/login?next=${encodeURIComponent(`/join?code=${code}`)}`);
      return;
    }
    setJoining(true);
    setJoinError(null);
    api
      .joinByShareLink(code)
      .then(async (result) => {
        setJoined(true);
        const list = await api.listWorkspaces().catch(() => [] as Workspace[]);
        queryClient.setQueryData(workspaceKeys.list(), list);
        setTimeout(() => {
          router.push(`/${result.workspace_slug || result.workspace_id}/issues`);
        }, 1200);
      })
      .catch(async (e) => {
        const code = errorCode(e);
        const msg = e instanceof Error ? e.message : "";
        if (msg.includes("already a member")) {
          // Already joined: go straight to the workspace this invite points at
          // rather than the account's first workspace.
          if (info?.workspace_slug) {
            router.push(`/${info.workspace_slug}/issues`);
            return;
          }
          try {
            const workspaces = await api.listWorkspaces();
            queryClient.setQueryData(workspaceKeys.list(), workspaces as any);
            const first = workspaces[0];
            if (first) {
              router.push(`/${first.slug}/issues`);
              return;
            }
          } catch {
            // Fall through to the home redirect below.
          }
          router.push("/");
          return;
        }
        setJoining(false);
        if (code === "seat_capacity_full") {
          setJoinError("成员席位已全部使用，请联系工作区管理员增加席位后重试。");
          return;
        }
        if (code === "seat_capacity_unavailable") {
          setJoinError("无法验证成员容量，请重试。");
          return;
        }
        setJoinError(msg || "加入工作区失败，链接可能已过期。");
      });
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-4">
      <Card className="w-full max-w-md">
        <CardContent className="space-y-4 pt-6">
          {joined ? (
            <>
              <h1 className="text-title-lg font-semibold text-center">已加入！</h1>
              <p className="text-center text-muted-foreground">正在进入工作区……</p>
            </>
          ) : infoError ? (
            <>
              <h1 className="text-title-lg font-semibold text-center">出错了</h1>
              <p className="text-center text-muted-foreground">{infoError}</p>
              <div className="flex justify-center pt-2">
                <Button variant="outline" onClick={() => router.push("/")}>
                  返回首页
                </Button>
              </div>
            </>
          ) : !info ? (
            <div className="py-6 text-center text-muted-foreground">正在加载邀请信息……</div>
          ) : (
            <>
              <h1 className="text-title-lg font-semibold text-center">
                邀请你加入 {info.workspace_name}
              </h1>
              {info.creator_name && (
                <p className="text-center text-muted-foreground">
                  邀请人：{info.creator_name}
                </p>
              )}
              <p className="flex items-center justify-center gap-2 text-center text-body text-muted-foreground">
                <span>你将以此身份加入工作区</span>
                <Badge variant="outline">
                  {info.role === "admin" ? "管理员" : "成员"}
                </Badge>
              </p>
              {!user && (
                <p className="text-center text-body text-muted-foreground">
                  登录后即可加入此工作区。
                </p>
              )}
              {joinError && (
                <p className="text-center text-body text-destructive">{joinError}</p>
              )}
              <div className="flex justify-center gap-2 pt-2">
                <Button onClick={handleJoin} disabled={joining}>
                  {joining ? "正在加入……" : user ? "加入工作区" : "登录后加入"}
                </Button>
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export default function JoinPage() {
  return (
    <Suspense fallback={<div className="flex min-h-screen items-center justify-center">正在加载……</div>}>
      <JoinInner />
    </Suspense>
  );
}
