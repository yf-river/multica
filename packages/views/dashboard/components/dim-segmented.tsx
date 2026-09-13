"use client";

// 卡片内的「日/周」维度切换控件，只渲染当前时间段允许的维度。
import { useT } from "../../i18n";
import { Segmented, type Dim } from "./dashboard-shared";

/**
 * 日/周切换控件，作用范围限定在它所在的那张卡片。
 *
 * 只渲染当前时间段允许的维度，这正好消除了它以前隐藏的耦合：它曾经住在页面头部、
 * 挨着时间段控件，一旦选了时间段不支持的维度就会悄悄改写时间段——带动页面上每个
 * KPI 一起变（MUL-5759）。现在不合法选项直接不显示，也就没有东西需要重置。
 *
 * 如果只允许一种维度，则什么都不渲染：只有一个选项的开关是没法操作的控件，
 * 而且图表标题（如 "Weekly cost"）本身已经说明了当前是哪种粒度。
 *
 * @param allowedDims 当前时间段允许的维度列表
 * @param value 当前选中的维度
 * @param onChange 切换维度时的回调
 */
export function DimSegmented({
  allowedDims,
  value,
  onChange,
}: {
  allowedDims: readonly Dim[];
  value: Dim;
  onChange: (dim: Dim) => void;
}) {
  const { t } = useT("usage");
  if (allowedDims.length < 2) return null;

  const label: Record<Dim, string> = {
    daily: t(($) => $.dim.daily),
    weekly: t(($) => $.dim.weekly),
  };

  return (
    <Segmented
      label={t(($) => $.dim.label)}
      value={value}
      onChange={onChange}
      options={allowedDims.map((dim) => ({ label: label[dim], value: dim }))}
    />
  );
}
