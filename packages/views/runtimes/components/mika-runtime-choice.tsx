// ============================================================================
// Mika 运行目标选择器（MikaRuntimeChoice）——「让 Mika 跑在哪台 runtime、
// 用哪个模型」的唯一决策点
//
// 角色定位：Web CLI 对话框与 Runtimes 页都会问同一个问题——把 Mika 指派给哪
// 台 runtime、选什么模型——并必须对「选中的 runtime」与「选中的模型」达成
// 一致。把这一整套选择（含换 runtime 时重置模型的规则）收敛到这一个组件，
// 调用方就不会各自实现、漏掉重置逻辑。
//
// 两种布局：dropdown（默认）用紧凑下拉选 runtime + 模型下拉；list 把 runtime
// 列成可滚动行（用于 `multica setup` 后一台台冒出来的机器引导场景，收起的
// 下拉反而藏掉了这个对话框正要给的反馈）。
//
// 数据流：父组件传入候选 runtimes 与受控的 value/onChange。选完 runtime 后
// 的模型候选由 ModelDropdown 依据该 runtime 动态拉取；切换 runtime 时旧模型
// 一并清空（见 selectRuntime）。
// ============================================================================
"use client";

import type { AgentRuntime } from "@multica/core/types";
import { ModelDropdown } from "../../agents/components/model-dropdown";
import { RuntimePicker } from "../../agents/components/runtime-picker";
import { CompactRuntimeRow } from "./compact-runtime-row";

// 一次完整选择：目标 runtime id + 目标模型。
export interface MikaRuntimeSelection {
  runtimeId: string;
  /** 模型名；为空表示「用该 runtime 的默认模型」。 */
  model: string;
}

/**
 * 「让 Mika 跑在哪台 runtime、用哪个模型」的选择器。
 *
 * Web CLI 对话框和 Runtimes 页都会走到这个同样的决策，两者必须对选中的
 * runtime 和模型保持一致。把「换 runtime 时清掉旧模型」的重置规则收敛在这里，
 * 就不会发生切换 runtime 后把一台 runtime 上根本不存在的模型悄悄带过去的事。
 *
 * `layout` 之所以存在，是因为两种场景的呈现方式确实不同，而非逻辑不同：
 * CLI 对话框用列表逐台展示机器——那是 `multica setup` 之后机器一台台出现、
 * 需要逐个确认的时刻；收起的下拉恰好把这段对话框想给的反馈藏掉了。
 */
export function MikaRuntimeChoice({
  runtimes,
  runtimesLoading,
  currentUserId = null,
  value,
  onChange,
  disabled = false,
  layout = "dropdown",
}: {
  runtimes: AgentRuntime[];
  runtimesLoading?: boolean;
  /** 仅 dropdown 布局使用，用于在选项里标注 runtime 的归属者。 */
  currentUserId?: string | null;
  value: MikaRuntimeSelection;
  onChange: (next: MikaRuntimeSelection) => void;
  disabled?: boolean;
  layout?: "dropdown" | "list";
}) {
  const selected = runtimes.find((rt) => rt.id === value.runtimeId) ?? null;

  const selectRuntime = (runtimeId: string) => {
    // 模型是跟着 runtime 走的——给上一台 runtime 选的模型在另一台上未必存在。
    // 这个重置逻辑必须在换台时执行，放在这里才能保证任何调用方都不会漏掉它。
    onChange({
      runtimeId,
      model: runtimeId === value.runtimeId ? value.model : "",
    });
  };

  return (
    <div className="flex flex-col gap-3">
      {layout === "list" ? (
        // 上限约 4 行并允许滚动：成员注册了多台机器时，上方的安装命令要始终
        // 够得着，不能让一排排 runtime 把它们挤出可视区。
        <div className="flex max-h-[240px] flex-col gap-2 overflow-y-auto">
          {runtimes.map((rt) => (
            <CompactRuntimeRow
              key={rt.id}
              runtime={rt}
              selected={rt.id === value.runtimeId}
              onSelect={() => selectRuntime(rt.id)}
              disabled={disabled}
            />
          ))}
        </div>
      ) : (
        <RuntimePicker
          runtimes={runtimes}
          runtimesLoading={runtimesLoading}
          members={[]}
          currentUserId={currentUserId}
          selectedRuntimeId={value.runtimeId}
          onSelect={selectRuntime}
          disabled={disabled}
        />
      )}
      <ModelDropdown
        runtimeId={value.runtimeId || null}
        runtimeOnline={selected?.status === "online"}
        value={value.model}
        onChange={(model) => onChange({ ...value, model })}
        disabled={!value.runtimeId || disabled}
      />
    </div>
  );
}
