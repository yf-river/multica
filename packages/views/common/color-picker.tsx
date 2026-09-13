// 通用颜色选择器：饱和度/明度区域、色相滑杆、取色器（浏览器支持时）、hex 输入和预设调色板，值以 #rrggbb 传输。
"use client";

import { useEffect, useState } from "react";
import { Dices, Pipette } from "lucide-react";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import {
  type Hsv,
  hexToHsv,
  hsvToHex,
  normalizeHex,
  randomOptionColor,
} from "./color-utils";

/**
 * 所有取色场景（标签、属性选项）共用的默认调色板。统一这份清单才能让颜色在
 * 整个产品里保持一致——先从这里挑，只有需要特定颜色时才去用区域/hex 自定义。
 */
export const COLOR_PICKER_PRESETS = [
  "#6b7280",
  "#ef4444",
  "#f97316",
  "#eab308",
  "#22c55e",
  "#14b8a6",
  "#3b82f6",
  "#6366f1",
  "#a855f7",
  "#ec4899",
] as const;

const HUE_GRADIENT =
  "linear-gradient(to right, #f00, #ff0 17%, #0f0 33%, #0ff 50%, #00f 67%, #f0f 83%, #f00)";

function clamp01(value: number): number {
  return Math.min(1, Math.max(0, value));
}

/**
 * 通用颜色选择器：饱和度/明度区域、色相滑杆、取色器（浏览器支持时）、hex
 * 输入，以及带一键"随机"的默认调色板。值以 `#rrggbb` hex 字符串传输。
 *
 * `trigger` 是打开弹层的元素（通常是色块按钮），通过 Base UI 的 render prop
 * 传入，和其他选择器一致。
 * @param value 当前颜色值。
 * @param onChange 颜色变化回调。
 * @param trigger 触发弹层的元素。
 * @param align 弹层对齐方式。
 */
export function ColorPicker({
  value,
  onChange,
  trigger,
  align = "start",
}: {
  value: string;
  onChange: (color: string) => void;
  trigger: React.ReactElement;
  align?: "start" | "center" | "end";
}) {
  return (
    <Popover>
      <PopoverTrigger render={trigger} />
      <PopoverContent align={align} className="w-72 p-3">
        <ColorPickerPanel value={value} onChange={onChange} />
      </PopoverContent>
    </Popover>
  );
}

/**
 * 不带弹层的颜色面板——导出给测试和未来的内联嵌入使用。
 * @param value 当前颜色值。
 * @param onChange 颜色变化回调。
 */
export function ColorPickerPanel({
  value,
  onChange,
}: {
  value: string;
  onChange: (color: string) => void;
}) {
  const { t } = useT("common");
  const [hsv, setHsv] = useState<Hsv>(
    () => hexToHsv(value) ?? { h: 217, s: 0.72, v: 0.96 },
  );
  // 用户输入过程中的本地 hex 文本；为 null 时直接镜像 `value`。
  const [hexDraft, setHexDraft] = useState<string | null>(null);
  const [eyedropperSupported] = useState(
    () => typeof window !== "undefined" && "EyeDropper" in window,
  );

  // 当颜色从区域/滑杆之外变化（点击预设、随机、hex 输入、调用方更新）时同步
  // 内部 HSV。hex 无法表达无色相的色相、也无法表达黑色的饱和度，所以保留这些
  // 部分，避免区域/滑杆跳变。
  useEffect(() => {
    setHsv((previous) => {
      if (hsvToHex(previous) === normalizeHex(value)) return previous;
      const next = hexToHsv(value);
      if (!next) return previous;
      return {
        h: next.s === 0 || next.v === 0 ? previous.h : next.h,
        s: next.v === 0 ? previous.s : next.s,
        v: next.v,
      };
    });
  }, [value]);

  const commit = (next: Hsv) => {
    setHsv(next);
    setHexDraft(null);
    onChange(hsvToHex(next));
  };

  const handleAreaPointer = (event: React.PointerEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    commit({
      h: hsv.h,
      s: clamp01((event.clientX - rect.left) / rect.width),
      v: 1 - clamp01((event.clientY - rect.top) / rect.height),
    });
  };

  const handleHuePointer = (event: React.PointerEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    commit({
      ...hsv,
      h: 360 * clamp01((event.clientX - rect.left) / rect.width),
    });
  };

  const pickFromScreen = async () => {
    const EyeDropperCtor = (
      window as unknown as {
        EyeDropper?: new () => { open: () => Promise<{ sRGBHex: string }> };
      }
    ).EyeDropper;
    if (!EyeDropperCtor) return;
    try {
      const result = await new EyeDropperCtor().open();
      const normalized = normalizeHex(result.sRGBHex);
      if (normalized) {
        setHexDraft(null);
        onChange(normalized);
      }
    } catch {
      // 用户取消了取色器
    }
  };

  const currentHex = normalizeHex(value) ?? hsvToHex(hsv);

  return (
    <div className="flex flex-col gap-2.5">
      {/* 饱和度 / 明度区域 */}
      <div
        role="slider"
        tabIndex={0}
        aria-label={t(($) => $.color_picker.saturation)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(hsv.v * 100)}
        aria-valuetext={`${Math.round(hsv.s * 100)}%, ${Math.round(hsv.v * 100)}%`}
        className="relative h-36 w-full cursor-crosshair touch-none rounded-md ring-1 ring-inset ring-black/10"
        style={{
          backgroundColor: `hsl(${hsv.h} 100% 50%)`,
          backgroundImage:
            "linear-gradient(to top, #000, transparent), linear-gradient(to right, #fff, transparent)",
        }}
        onPointerDown={(event) => {
          event.currentTarget.setPointerCapture(event.pointerId);
          handleAreaPointer(event);
        }}
        onPointerMove={(event) => {
          if (event.buttons & 1) handleAreaPointer(event);
        }}
        onKeyDown={(event) => {
          const step = event.shiftKey ? 0.1 : 0.02;
          if (event.key === "ArrowLeft") commit({ ...hsv, s: clamp01(hsv.s - step) });
          else if (event.key === "ArrowRight") commit({ ...hsv, s: clamp01(hsv.s + step) });
          else if (event.key === "ArrowUp") commit({ ...hsv, v: clamp01(hsv.v + step) });
          else if (event.key === "ArrowDown") commit({ ...hsv, v: clamp01(hsv.v - step) });
          else return;
          event.preventDefault();
        }}
      >
        <span
          className="pointer-events-none absolute size-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-white shadow-[0_0_0_1px_rgba(0,0,0,0.2)]"
          style={{
            left: `${hsv.s * 100}%`,
            top: `${(1 - hsv.v) * 100}%`,
            backgroundColor: currentHex,
          }}
        />
      </div>

      {/* 取色器 + 色相滑杆 */}
      <div className="flex items-center gap-2.5">
        {eyedropperSupported && (
          <Button
            type="button"
            variant="outline"
            size="icon-sm"
            aria-label={t(($) => $.color_picker.eyedropper)}
            title={t(($) => $.color_picker.eyedropper)}
            onClick={pickFromScreen}
          >
            <Pipette className="size-3.5" />
          </Button>
        )}
        <div
          role="slider"
          tabIndex={0}
          aria-label={t(($) => $.color_picker.hue)}
          aria-valuemin={0}
          aria-valuemax={360}
          aria-valuenow={Math.round(hsv.h)}
          className="relative h-3 min-w-0 flex-1 cursor-pointer touch-none rounded-full ring-1 ring-inset ring-black/10"
          style={{ background: HUE_GRADIENT }}
          onPointerDown={(event) => {
            event.currentTarget.setPointerCapture(event.pointerId);
            handleHuePointer(event);
          }}
          onPointerMove={(event) => {
            if (event.buttons & 1) handleHuePointer(event);
          }}
          onKeyDown={(event) => {
            const step = event.shiftKey ? 15 : 3;
            if (event.key === "ArrowLeft" || event.key === "ArrowDown")
              commit({ ...hsv, h: Math.max(0, hsv.h - step) });
            else if (event.key === "ArrowRight" || event.key === "ArrowUp")
              commit({ ...hsv, h: Math.min(360, hsv.h + step) });
            else return;
            event.preventDefault();
          }}
        >
          <span
            className="pointer-events-none absolute top-1/2 size-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-white shadow-[0_0_0_1px_rgba(0,0,0,0.2)]"
            style={{
              left: `${(hsv.h / 360) * 100}%`,
              backgroundColor: `hsl(${hsv.h} 100% 50%)`,
            }}
          />
        </div>
      </div>

      {/* Hex 输入 */}
      <div className="flex items-center gap-2.5">
        <span
          className="size-6 shrink-0 rounded-full ring-1 ring-inset ring-black/10"
          style={{ backgroundColor: currentHex }}
        />
        <Input
          value={hexDraft ?? currentHex.toUpperCase()}
          aria-label={t(($) => $.color_picker.hex)}
          spellCheck={false}
          className="h-8 font-mono text-caption"
          onChange={(event) => {
            setHexDraft(event.target.value);
            const normalized = normalizeHex(event.target.value);
            if (normalized) onChange(normalized);
          }}
          onBlur={() => setHexDraft(null)}
          onKeyDown={(event) => {
            if (event.key === "Enter") setHexDraft(null);
          }}
        />
      </div>

      {/* 默认调色板 + 随机 */}
      <div className="flex items-center justify-between">
        <span className="text-caption font-medium text-muted-foreground">
          {t(($) => $.color_picker.presets)}
        </span>
        <button
          type="button"
          className="flex items-center gap-1 text-caption text-muted-foreground transition-colors hover:text-foreground"
          onClick={() => {
            setHexDraft(null);
            onChange(randomOptionColor(value));
          }}
        >
          <Dices className="size-3.5" />
          {t(($) => $.color_picker.random)}
        </button>
      </div>
      <div className="flex items-center justify-between">
        {COLOR_PICKER_PRESETS.map((preset) => (
          <button
            key={preset}
            type="button"
            aria-label={preset}
            aria-pressed={currentHex === preset}
            onClick={() => {
              setHexDraft(null);
              onChange(preset);
            }}
            className={cn(
              "size-5 rounded-full transition-transform hover:scale-110",
              currentHex === preset &&
                "ring-2 ring-ring ring-offset-2 ring-offset-surface-raised",
            )}
            style={{ backgroundColor: preset }}
          />
        ))}
      </div>
    </div>
  );
}
