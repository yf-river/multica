"use client";

// 问题订阅 hook：读取某个问题的订阅者列表，支持切换订阅、退订整个问题树，
// 并通过 WS 实时同步别人订阅/退订的操作。这里还是判断「当前用户是否在关注、为什么在关注」
// 的地方，订阅按钮和「谁在关注」说明文案都靠它。
import { useCallback, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { IssueSubscriber } from "@multica/core/types";
import type {
  SubscriberAddedPayload,
  SubscriberRemovedPayload,
} from "@multica/core/types";
import { issueSubscribersOptions, issueKeys } from "@multica/core/issues/queries";
import {
  useToggleIssueSubscriber,
  useUnsubscribeFromIssueSubtree,
} from "@multica/core/issues/mutations";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
// 从定义该错误的模块直接引入，而不是走 "@multica/core/api" 的总出口：总出口会把
// 客户端单例和 ws-client 一起拖进每个使用本 hook 的模块依赖图，明显拖慢测试启动，
// 还让某个对时序敏感的测试套件超过 waitFor 的预算。
import { ApiError } from "@multica/core/api/client";
import { toast } from "sonner";
import { useT } from "../../i18n";

/**
 * 只把「部署版本错位」产生的 404 判为 true：也就是后端还没有 /unsubscribe/subtree
 * 这个路由、由 chi 用纯文本应答未知 ROUTE 的情况。
 *
 * 并不是所有 404 都是它。同一个接口在当前版本的后端上，当问题被删除或不可见时
 * 会返回结构化的 JSON 404，这时让用户去「等下一次更新」是错误的建议。
 * ApiError.body 是区分二者的依据：parseErrorBody 在响应不是 JSON 时会让它保持
 * undefined，而服务端真正返回了错误文档时它会带上内容（MUL-5483 review 第 8 轮）。
 */
function isMissingRouteError(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404 && err.body === undefined;
}

/**
 * 管理某个问题的订阅：读取订阅者列表、切换订阅、退订整个问题树，并实时同步他人操作。
 * @param issueId - 问题 id。
 * @param userId - 当前用户 id（成员），用于判断「我」是否已订阅。
 */
export function useIssueSubscribers(issueId: string, userId?: string) {
  const qc = useQueryClient();
  const { t } = useT("issues");
  const { data: subscribers = [], isSuccess } = useQuery(
    issueSubscribersOptions(issueId),
  );

  const toggleMutation = useToggleIssueSubscriber(issueId);
  const subtreeMutation = useUnsubscribeFromIssueSubtree(issueId);

  // 断线重连后的恢复：让查询失效，下次渲染时重新拉取。
  useWSReconnect(
    useCallback(() => {
      qc.invalidateQueries({ queryKey: issueKeys.subscribers(issueId) });
    }, [qc, issueId]),
  );

  // --- WS 事件处理器（把其他用户的操作同步进服务端缓存） ---

  useWSEvent(
    "subscriber:added",
    useCallback(
      (payload: unknown) => {
        const p = payload as SubscriberAddedPayload;
        if (p.issue_id !== issueId) return;
        qc.setQueryData<IssueSubscriber[]>(
          issueKeys.subscribers(issueId),
          (old) => {
            if (!old) return old;
            const existing = old.find(
              (s) => s.user_id === p.user_id && s.user_type === p.user_type,
            );
            // 服务端只在「原因变化」时才对一个已存在的订阅者重新广播——比如某个
            // 被委托人被指派、被提及或收到评论后，会从降级档升级。这里要就地修补
            // 而不是直接放弃，否则「通过智能体关注」的徽标会一直宣称一个已不成立的
            // 委托关系（MUL-5483）。
            if (existing) {
              if (existing.reason === p.reason) return old;
              return old.map((s) =>
                s.user_id === p.user_id && s.user_type === p.user_type
                  ? { ...s, reason: p.reason as IssueSubscriber["reason"] }
                  : s,
              );
            }
            return [
              ...old,
              {
                issue_id: p.issue_id,
                user_type: p.user_type as "member" | "agent",
                user_id: p.user_id,
                reason: p.reason as IssueSubscriber["reason"],
                created_at: new Date().toISOString(),
              },
            ];
          },
        );
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "subscriber:removed",
    useCallback(
      (payload: unknown) => {
        const p = payload as SubscriberRemovedPayload;
        if (p.issue_id !== issueId) return;
        qc.setQueryData<IssueSubscriber[]>(
          issueKeys.subscribers(issueId),
          (old) =>
            old?.filter(
              (s) =>
                !(s.user_id === p.user_id && s.user_type === p.user_type),
            ),
        );
      },
      [qc, issueId],
    ),
  );

  // --- 变更订阅的 mutation ---

  const ownSubscription = subscribers.find(
    (s) => s.user_type === "member" && s.user_id === userId,
  );
  const isSubscribed = !!ownSubscription;
  // `subscribers` 和 `isSubscribed` 都来自 `data ?? []`，所以在查询返回之前它们
  // 一律读作「没有人订阅」——包括对那些其实已订阅的人。凡是基于它们派生的控件
  // 都必须先看 `subscriptionKnown` 再决定行为，而不是直接渲染默认态；订阅者选择器
  // 也一样，不只是订阅按钮：一个对已订阅者显示「未勾选」的行，点击后发出的是一次
  // 显式的订阅，会把订阅原因改写回 'manual'、并清掉任何免打扰范围
  // （server/pkg/db/queries/subscriber.sql）。查询失败同样视为「未知」；
  // 只有查询成功返回才算数（MUL-5714）。
  const subscriptionKnown = isSuccess;
  // 当前用户为什么在关注。用于驱动「你的智能体以你的名义创建了这个」的解释——一个
  // 没人记得主动订阅过的订阅关系，不说明原因就会显得产品很冒犯（MUL-5483）。
  const subscriptionReason = ownSubscription?.reason;

  // 让直接切换订阅的请求串行执行。禁用按钮能覆盖一般情况，但 React Query 是在
  // 微任务里才把 `isPending` 落定的，所以同一帧内的两次点击都会命中一个还没被禁用
  // 的控件。交叠的切换正是 useToggleIssueSubscriber 的整表乐观快照扛不住的情况：
  // 第二次调用会对第一次打的补丁做快照，失败时回滚到的是它而不是服务端状态（MUL-5714）。
  const toggleInFlight = useRef(false);

  // useToggleIssueSubscriber 里的乐观补丁在失败时会自行回滚，让那一行恰好回到
  // 原来的样子——跟按钮从未被点过无法区分。所以要明说一句（MUL-5714）。
  const toggleSubscriber = useCallback(
    (
      subUserId: string,
      userType: "member" | "agent",
      currentlySubscribed: boolean,
    ) => {
      if (toggleInFlight.current) return;
      toggleInFlight.current = true;
      toggleMutation.mutate(
        {
          userId: subUserId,
          userType,
          subscribed: currentlySubscribed,
        },
        {
          onError: () =>
            toast.error(t(($) => $.detail.subscription_update_failed)),
          // 在 mutation 自己的 onSettled 之后运行，所以下一次切换开始时缓存已经失效完毕。
          onSettled: () => {
            toggleInFlight.current = false;
          },
        },
      );
    },
    [toggleMutation, t],
  );

  const toggleSubscribe = useCallback(() => {
    if (userId) toggleSubscriber(userId, "member", isSubscribed);
  }, [userId, isSubscribed, toggleSubscriber]);

  // 订阅状态归服务端所有，失败时那一行根本不会变，界面上什么都不会动。没有明确的
  // 提示的话，用户会把一次失败的退订当成一个没反应的按钮，然后一直重试。
  //
  // 成功也需要一条提示，理由相同，而且只在这里：这个 mutation 刻意不做乐观更新
  // （它要退役的是一棵未知数量的子孙订阅），所以不像直接切换那样有标签或头像的翻转
  // 来确认点击生效（MUL-5714）。
  const unsubscribeFromSubtree = useCallback(() => {
    if (!userId) return;
    subtreeMutation.mutate(
      { userId, userType: "member" },
      {
        onSuccess: () =>
          toast.success(t(($) => $.detail.unsubscribe_subtree_succeeded)),
        onError: (err) =>
          toast.error(
            isMissingRouteError(err)
              ? t(($) => $.detail.unsubscribe_subtree_unsupported)
              : t(($) => $.detail.unsubscribe_subtree_failed),
          ),
      },
    );
  }, [userId, subtreeMutation, t]);

  return {
    subscribers,
    subscriptionKnown,
    isSubscribed,
    subscriptionReason,
    // 界面基于这些串行化信号，是让 useToggleIssueSubscriber 的整表乐观快照保持安全
    // 的关键：并发的切换会互相拿对方的进行中补丁做快照，回滚时退错列表。
    togglePending: toggleMutation.isPending,
    subtreePending: subtreeMutation.isPending,
    toggleSubscribe,
    toggleSubscriber,
    unsubscribeFromSubtree,
  };
}
