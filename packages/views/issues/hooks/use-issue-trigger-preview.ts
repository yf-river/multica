"use client";

// 问题触发预览 hook：在创建/指派/改状态真正发请求之前，调用后端统一的
// 「这次操作是否会启动一次智能体运行」判断接口做预览，给指派器/创建弹窗/状态菜单等
// 四个入口共享同一个结论（MUL-3375）。
import { useMemo } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import type { IssueAssigneeType, IssueStatus, IssueTriggerPreviewItem } from "@multica/core/types";

/** useIssueTriggerPreview 的入参。 */
export interface UseIssueTriggerPreviewParams {
  /** 要评估的既有问题 id（单个指派/状态改动，或一批）。 */
  issueIds?: string[];
  /** 是否为「还没落库的问题」做预览（创建弹窗里按指派人/状态预判）。 */
  isCreate?: boolean;
  assigneeType?: IssueAssigneeType | null;
  assigneeId?: string | null;
  status?: IssueStatus;
  /** 调用方开关——例如只在选择器/弹窗打开时才发起请求。 */
  enabled?: boolean;
}

/** useIssueTriggerPreview 返回的预览结果。 */
export interface UseIssueTriggerPreviewResult {
  triggers: IssueTriggerPreviewItem[];
  totalCount: number;
  isLoading: boolean;
  /** 当每个触发目标所在的运行时都能渲染交接便签时为 true，此时便签输入框才安全可用。
   *  只要任何一个已启动的运行可能丢弃便签就为 false。 */
  handoffSupported: boolean;
}

const EMPTY: IssueTriggerPreviewItem[] = [];

/** 生成预览签名字符串：只由影响判断结果的输入（指派对象/状态/是否创建）决定，用于查询缓存 key。 */
function previewSignature(params: UseIssueTriggerPreviewParams): string {
  return JSON.stringify({
    ids: [...(params.issueIds ?? [])].sort(),
    create: params.isCreate ?? false,
    at: params.assigneeType ?? null,
    aid: params.assigneeId ?? null,
    status: params.status ?? null,
  });
}

/** 通过 POST /api/issues/preview-trigger 读取统一的后端判断，让四个入口永远不用各自
 *  重新实现「这次会不会启动一次运行」（MUL-3375）。
 *
 *  结论只会随输入（指派人 / 状态）变化，所以查询只在签名变化时重新拉取——刻意不让
 *  WS 任务事件来失效它。指派来源（创建 / 换指派人）在入队前会取消已有任务，其结论不可能
 *  因任务事件而变；状态来源的去重逻辑可能会，但预览只是参考性的，真正写入时后端会
 *  再权威地判断一次，所以偶发过期的状态标签无伤大雅——远比每次工作区任务事件都把
 *  每个挂载中的预览拉一遍要好（那次可见的闪烁就是 MUL-3375 的来源）。
 *
 *  数据处理方式与评论触发预览一致：用 keepPreviousData，切换输入时原地换答案而不是
 *  先塌掉；只有真正的首次加载（没有先前数据）才计为 loading。 */
export function useIssueTriggerPreview(
  params: UseIssueTriggerPreviewParams,
): UseIssueTriggerPreviewResult {
  const hasTarget =
    (!!params.assigneeType && !!params.assigneeId) ||
    !!params.status ||
    (params.isCreate ?? false);
  const enabled = (params.enabled ?? true) && hasTarget;

  const signature = useMemo(() => previewSignature(params), [params]);

  const previewQuery = useQuery({
    queryKey: issueKeys.issueTriggerPreview(signature),
    queryFn: () =>
      api.previewIssueTrigger({
        issueIds: params.issueIds,
        isCreate: params.isCreate,
        assigneeType: params.assigneeType,
        assigneeId: params.assigneeId,
        status: params.status,
      }),
    enabled,
    retry: false,
    staleTime: 0,
    // 新的签名（指派人/状态切换）重新拉取时保留上一条结论可见，提示文字原地切换而不是先塌掉。
    placeholderData: keepPreviousData,
  });

  const triggers = previewQuery.data?.triggers ?? EMPTY;
  return {
    triggers,
    totalCount: previewQuery.data?.total_count ?? 0,
    // 只有首次加载（无先前数据）才算 "loading"；后台/占位数据的重新拉取不算，
    // 这样挂在它上面的展示动画不会在拉取中途塌掉。
    isLoading: enabled && previewQuery.isLoading,
    handoffSupported: triggers.length > 0 && triggers.every((t) => t.handoff_supported === true),
  };
}
