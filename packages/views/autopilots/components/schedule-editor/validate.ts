// 定时器计划的"提交前校验"与"拒绝反馈"：编辑器内联错误只覆盖预览已落定的表达式；
// 在预览请求还在飞行中时就点保存，会先持久化再创建触发器失败，留下一个永不运行的
// autopilot。因此所有写路径在真正写入前还要再过一道与服务器确认的闸。本文件集中
// 提供：对服务器 400 的归类、确认查询、toast 播报，以及可复用的提交闸 Hook。

import { useState } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@multica/core/api";
import { cronPreviewOptions } from "@multica/core/autopilots/queries";
import { useT } from "../../../i18n";
import type { ScheduleConfig } from "./model";
import { toCron } from "./cron-mapping";

/** 服务器的拒绝，按编辑器的方式拆开：哪个输入出错（cron 框还是时区选择器），
 *  以及解析器自己的话。 */
export interface ScheduleRejection {
  code: "invalid_cron" | "invalid_timezone";
  detail: string;
}

/** 从预览的 400 上读出服务器的归类。除显式 invalid_timezone 标签外一律归咎于
 *  cron。 */
export function classifyScheduleRejection(err: ApiError): ScheduleRejection {
  const body = typeof err.body === "object" && err.body !== null ? err.body : {};
  const code = (body as { code?: unknown }).code;
  return {
    code: code === "invalid_timezone" ? "invalid_timezone" : "invalid_cron",
    detail: err.message,
  };
}

/**
 * 问服务器是否接受这份计划，返回它的拒绝（接受时返回 null）。
 *
 * 编辑器的内联错误只覆盖预览已经落定的表达式：打了一串非法 cron 就立刻点保存，
 * 会在预览请求还在飞行中时于 blur 时提交草稿，提交闸仍是开的。那种状态下创建
 * autopilot 会先持久化、随后创建触发器失败，留下一个永不运行的 autopilot——所以
 * 提交路径在写入前必须再校验一次。
 *
 * 传输层故障归为 null：预览接口不可达不该阻止保存一份服务器本会接受的计划。
 * @param queryClient React Query 客户端。
 * @param wsId 工作区 ID。
 * @param config 要校验的调度配置。
 * @returns 拒绝详情；接受或传输故障时为 null。
 */
export async function findScheduleRejection(
  queryClient: QueryClient,
  wsId: string,
  config: ScheduleConfig,
): Promise<ScheduleRejection | null> {
  try {
    await queryClient.fetchQuery(cronPreviewOptions(wsId, toCron(config), config.timezone));
    return null;
  } catch (err) {
    if (!(err instanceof ApiError) || err.status !== 400) return null;
    return classifyScheduleRejection(err);
  }
}

/** 用两个提交路径必须一致的方式播报拒绝：标题点名出错的那个控件（cron 与时区固定
 *  在不同控件里），解析器自己的话放下面——不翻译，但它是唯一说明到底错在哪的
 *  文本。 */
export function toastScheduleRejection(
  t: ReturnType<typeof useT<"autopilots">>["t"],
  rejection: ScheduleRejection,
): void {
  toast.error(
    rejection.code === "invalid_timezone"
      ? t(($) => $.schedule_editor.timezone_invalid)
      : t(($) => $.schedule_editor.cron_invalid),
    { description: rejection.detail },
  );
}

/**
 * 每个写计划的表单都需要的提交闸，收拢在一处：服务器拒绝过的计划绝不能提交，
 * 否则表单会持久化一个触发器随后 400、永不运行的 autopilot。
 *
 * - `scheduleValid` 门控提交按钮，由编辑器的 `onValidityChange` 与
 *   `ensureAccepted` 共同喂。
 * - `clearRejection` 要放进计划的 `onChange`：提交时的拒绝属于闸而非编辑器——
 *   编辑器自己的预览可能因为防抖还没触发而从未查询该表达式，那样没人会把
 *   Save 重新点亮。任何编辑都解除它；若新表达式也不合法，编辑器的内联错误会再次
 *   禁用 Save。
 * - `ensureAccepted` 执行写入前检查（见 findScheduleRejection）并自己 toast 拒绝
 *   内容；调用方只需回卷自己的提交状态。
 *
 * 让承载表单只在打开期间挂载：闸的状态跟它同生共死，一直挂着关闭态的表单会把
 * 过期的拒绝带进下一次打开。
 * @param wsId 工作区 ID。
 * @returns 提交闸的四件套。
 */
export function useScheduleSubmitGate(wsId: string): {
  scheduleValid: boolean;
  clearRejection: () => void;
  onValidityChange: (valid: boolean) => void;
  ensureAccepted: (config: ScheduleConfig) => Promise<boolean>;
} {
  const { t } = useT("autopilots");
  const queryClient = useQueryClient();
  const [scheduleValid, setScheduleValid] = useState(true);
  return {
    scheduleValid,
    clearRejection: () => setScheduleValid(true),
    onValidityChange: setScheduleValid,
    ensureAccepted: async (config: ScheduleConfig): Promise<boolean> => {
      const rejection = await findScheduleRejection(queryClient, wsId, config);
      if (rejection === null) return true;
      setScheduleValid(false);
      toastScheduleRejection(t, rejection);
      return false;
    },
  };
}
