"use client";
// 本文件是分段式时间输入（TimeInput）：把 HH:MM 拆成「时」「分」两个 2 位
// 数字段。键盘体验完整——上下箭头增减、直接敲数字补位、退格清空、左右箭头
// 在段间移动。改编自 openstatusHQ/time-picker（MIT）。

import * as React from "react";
import { Clock } from "lucide-react";

import { cn } from "@multica/ui/lib/utils";

/** 补零到 2 位：5 → "05" */
function pad2(n: number): string {
  return n.toString().padStart(2, "0");
}

/**
 * 把一个数字串校验/约束成合法值：非法输入回落到 min；超界时按是否 loop
 * 决定是「夹到边界」还是「回绕」（min ↔ max 循环）。统一返回补零的 2 位串。
 */
function getValidNumber(
  raw: string,
  { max, min = 0, loop = false }: { max: number; min?: number; loop?: boolean },
): string {
  let n = parseInt(raw, 10);
  if (isNaN(n)) return pad2(min);
  if (!loop) {
    if (n > max) n = max;
    if (n < min) n = min;
  } else {
    if (n > max) n = min;
    if (n < min) n = max;
  }
  return pad2(n);
}

/** 按 step 增减当前值并回绕（如 23 加 1 → 00），用于上下箭头 */
function arrowValue(current: string, step: number, min: number, max: number): string {
  const n = parseInt(current, 10);
  if (isNaN(n)) return pad2(min);
  return getValidNumber(String(n + step), { max, min, loop: true });
}

/** 把 "HH:MM" 拆开并按各自范围校验，得到 { hh, mm } 两个 2 位串 */
function splitTime(value: string, hourMin: number): { hh: string; mm: string } {
  const [rawH, rawM] = (value || "").split(":");
  const hh = getValidNumber(rawH ?? "0", { max: 23, min: hourMin });
  const mm = getValidNumber(rawM ?? "0", { max: 59 });
  return { hh, mm };
}

/** 单个段输入框（小时或分钟）的 props */
interface SegmentInputProps {
  /** 段的当前 2 位值 */
  value: string;
  // 本段取值范围。上下箭头会在范围内回绕，直接敲数字也会被夹到范围内——
  // 因此范围被邻居收窄的段（例如窗口的结束小时，它永远不能早于开始小时）
  // 无论是箭头还是直接敲数都出不了界。
  min: number;
  max: number;
  // 按退格时本段被清成的值。默认是 `min`——对「范围起点就是字段下限」的段
  // 来说这是中性值。但窗口的结束小时不是这种段：它的 min 在开始小时当前所在
  // 的位置，清成 min 等于把窗口塌缩回开始小时，而不是放开到当天结束。
  clearTo?: number;
  /** 值变化回调（注意：这里收到的是处理完的 2 位串） */
  onValueChange: (next: string) => void;
  /** 按 → 时把焦点让给右侧段 */
  onLeftFocus?: () => void;
  /** 按 → 时把焦点让给右侧段 */
  onRightFocus?: () => void;
  /** 禁用 */
  disabled?: boolean;
  /** 无障碍名称（如 "Hour"），由调用方按场景提供 */
  ariaLabel: string;
  // 段自身的 `data-slot`。容器如果要根据「段获得焦点」来点亮自己的边框，
  // 看的就是段上的这个标记——被聚焦的是段，而不是它们的包装层。
  slot?: string;
  /** 是否在挂载时聚焦该段 */
  autoFocus?: boolean;
}

const SegmentInput = React.forwardRef<HTMLInputElement, SegmentInputProps>(function SegmentInput(
  { value, min, max, clearTo, onValueChange, onLeftFocus, onRightFocus, disabled, ariaLabel, slot, autoFocus },
  ref,
) {
  // 两位数字的输入窗口：先敲的第一位会补一个前导 0 显示；2 秒内再敲第二位
  // 就合成两位数。第一位记在这里而不从 `value` 读回来——即使外层在两次按键
  // 之间对 value 做了夹取（clamp），也破坏不了正在输入的这个数。
  const [pendingFirst, setPendingFirst] = React.useState<string | null>(null);

  // 2 秒后没敲第二位就放弃合成
  React.useEffect(() => {
    if (pendingFirst === null) return;
    const t = setTimeout(() => setPendingFirst(null), 2000);
    return () => clearTimeout(t);
  }, [pendingFirst]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Tab") return;
    if (e.key === "ArrowRight") {
      e.preventDefault();
      onRightFocus?.();
      return;
    }
    if (e.key === "ArrowLeft") {
      e.preventDefault();
      onLeftFocus?.();
      return;
    }
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      const step = e.key === "ArrowUp" ? 1 : -1;
      onValueChange(arrowValue(value, step, min, max));
      setPendingFirst(null);
      return;
    }
    if (e.key >= "0" && e.key <= "9") {
      e.preventDefault();
      if (pendingFirst !== null) {
        onValueChange(getValidNumber(pendingFirst + e.key, { max, min }));
        setPendingFirst(null);
        onRightFocus?.();
      } else {
        // 第一位显示时会被夹到合法范围，但原样记住：比如一段从 09 开始的段，
        // 用户敲「1」仍要显示合法值，同时它还是他正要输入的 15 的十位。
        onValueChange(getValidNumber("0" + e.key, { max, min }));
        setPendingFirst(e.key);
      }
      return;
    }
    if (e.key === "Backspace" || e.key === "Delete") {
      e.preventDefault();
      onValueChange(pad2(clearTo ?? min));
      setPendingFirst(null);
    }
  };

  return (
    <input
      ref={ref}
      data-slot={slot}
      // 由调用方控制：只在用户刚切进这个控件时才 true，绝不在一开始的挂载
      // 就聚焦（见 TimeInput 的 autoFocus 说明）。
      autoFocus={autoFocus}
      type="text"
      inputMode="numeric"
      maxLength={2}
      value={value}
      disabled={disabled}
      aria-label={ariaLabel}
      onChange={() => {
        // 完全由 keydown 驱动，忽略原生 onChange。
      }}
      onKeyDown={handleKeyDown}
      onFocus={(e) => e.currentTarget.select()}
      // 两位合成窗口只属于「这一次进入该段」。如果不在这里清掉，这段敲的
      // 「1」和中间绕去别的控件后回来敲的「5」会被拼成 15。
      onBlur={() => setPendingFirst(null)}
      className="w-7 bg-transparent text-center text-body tabular-nums outline-none caret-transparent focus:text-foreground disabled:opacity-50"
    />
  );
});

/** TimeInput 的 props 定义 */
export interface TimeInputProps {
  /** 当前时间值，格式 "HH:MM"（受控） */
  value: string;
  /** 值变化回调，收到新的 "HH:MM" */
  onChange: (value: string) => void;
  /** 禁用整个输入 */
  disabled?: boolean;
  /** 外层 class 名 */
  className?: string;
  /** 是否显示时钟图标，默认 true */
  showIcon?: boolean;
  // 只渲染小时段（不渲染分钟与冒号）。用于分钟没有意义的场景，
  // 例如以「整点」为粒度的窗口边界。
  hourOnly?: boolean;
  // 该字段允许的最小小时。小时段在 [hourMin, 23] 内回绕和夹取，而不是
  // [0, 23]——窗口的结束时间不能早于开始时间，箭头从小时下限回绕时去的是
  // 23，而不是钉在开始小时上。
  hourMin?: number;
  // 退格清空小时时清成的值，用于该值不是 `hourMin` 的情况（见 SegmentInput
  // 的 clearTo）。
  hourClearTo?: number;
  // 两个段各自的无障碍名称。本包不带翻译，所以并排渲染多个时间字段时
  //（比如窗口的开始和结束），调用方要传入能区分它们的本地化名称。
  hourLabel?: string;
  minuteLabel?: string;
  // 小时/分钟段的 `data-slot`：给那些「靠内部获得焦点的控件来点亮自己边框」
  // 的容器用（见 InputGroupTimeInput）。
  segmentSlot?: string;
  // 挂载时聚焦小时段。调用方应把关：只在用户刚展开这个字段时才为 true，
  // 绝不能在表单首次渲染时触发。
  autoFocus?: boolean;
}

/**
 * TimeInput：HH:MM 分段时间输入。
 *
 * 内部是「时」「分」两个可独立编辑的 2 位输入段，用箭头/敲数字/退格编辑；
 * 外层是一个带边框和可选时钟图标的容器，点击空白处把焦点交给小时段。
 * hourOnly 时只保留小时段。
 *
 * @param props 见 TimeInputProps 定义
 * @returns 时间输入控件
 */
export function TimeInput({
  value,
  onChange,
  disabled,
  className,
  showIcon = true,
  hourOnly = false,
  hourMin = 0,
  hourClearTo,
  hourLabel = "Hour",
  minuteLabel = "Minute",
  segmentSlot,
  autoFocus,
}: TimeInputProps) {
  const { hh, mm } = splitTime(value, hourMin);
  const hourRef = React.useRef<HTMLInputElement>(null);
  const minuteRef = React.useRef<HTMLInputElement>(null);

  const setHour = (next: string) => onChange(`${next}:${mm}`);
  const setMinute = (next: string) => onChange(`${hh}:${next}`);

  return (
    <div
      data-slot="time-input"
      onClick={(e) => {
        if (e.target === e.currentTarget) {
          hourRef.current?.focus();
        }
      }}
      className={cn(
        "flex h-8 items-center gap-1 rounded-lg border border-input bg-transparent px-2.5 text-body transition-colors",
        "focus-within:border-ring focus-within:ring-3 focus-within:ring-ring/50",
        "dark:bg-input/30",
        disabled && "pointer-events-none cursor-not-allowed opacity-50",
        className,
      )}
    >
      {showIcon && (
        <Clock className="pointer-events-none size-3.5 shrink-0 text-muted-foreground" />
      )}
      <SegmentInput
        ref={hourRef}
        min={hourMin}
        max={23}
        clearTo={hourClearTo}
        value={hh}
        onValueChange={setHour}
        // hourOnly 时没有右侧可去：聚焦一个禁用的输入是无效操作，而发起这次
        // 聚焦的按键已经被吞掉，光标会看起来卡住不动。
        onRightFocus={hourOnly ? undefined : () => minuteRef.current?.focus()}
        disabled={disabled}
        ariaLabel={hourLabel}
        slot={segmentSlot}
        autoFocus={autoFocus}
      />
      {/* hourOnly 时整个去掉分钟段和冒号，而不是留一个不可用的 :00 维持宽度
        * 稳定：当边界只到整点时，一个灰掉的 :00 纯粹是浪费宽度。字段会随着
        * 切换到小时粒度而收窄——这本身就是「分钟级边界已经没了」的诚实信号。 */}
      {!hourOnly && (
        <>
          {/* -translate-y-[1.5px]：数字是等高大写字形，光学中心在冒号两点的
            * 上方，所以 items-center（对齐的是盒子而不是墨迹）会让冒号看起来
            * 偏低。把它往上顶一点到数字的中心。用 transform 而不是 margin，
            * 免得挤动其它元素。 */}
          <span className="pointer-events-none -translate-y-[1.5px] select-none text-muted-foreground">
            :
          </span>
          <SegmentInput
            ref={minuteRef}
            min={0}
            max={59}
            value={mm}
            onValueChange={setMinute}
            onLeftFocus={() => hourRef.current?.focus()}
            disabled={disabled}
            ariaLabel={minuteLabel}
            slot={segmentSlot}
          />
        </>
      )}
    </div>
  );
}
