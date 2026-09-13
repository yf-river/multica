// 定时器计划编辑器：把 cron 计划在"结构化表单"与"高级 cron 文本框"两种模式间
// 编辑。顶部字段编辑时间（固定时刻/每 N 小时/分钟 + 运行窗口）、日期（每天/每周/
// 每月）、时区；底部一个面板同时展示 cron 文本、白话回读与服务器端"未来运行时刻"
// 预览。凡表单能表达的，写回结构体并实时生成 cron；表单表达不了的表达式进入高级
// 模式只做文本编辑。所有对服务器的疑问（预览、合法性）统一以最终 cron 文本为准。

"use client";

import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ComponentProps,
  type ComponentType,
  type ReactNode,
} from "react";
import { useQuery } from "@tanstack/react-query";
import { Clock, Pencil } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Input } from "@multica/ui/components/ui/input";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupSelectTrigger,
  InputGroupText,
  InputGroupTimeInput,
} from "@multica/ui/components/ui/input-group";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@multica/ui/components/ui/select";
import { TimeInput } from "@multica/ui/components/ui/time-input";
import { cronPreviewOptions } from "@multica/core/autopilots/queries";
import { ApiError } from "@multica/core/api";
import { timezoneOptions } from "../../../common/timezone-select";
import { useDebouncedValue } from "../../../common/use-debounced-value";
import { SegmentedToggle } from "../../../common/segmented-toggle";
import { formatInTimeZone } from "../../../common/format-in-time-zone";
import { TimezonePicker } from "../pickers/timezone-picker";
import { useT } from "../../../i18n";
import type { DayPattern, ScheduleConfig, TimePattern } from "./model";
import { DAY_KEYS, pad2, timeParts } from "./model";
import {
  cronFields,
  extractTimezonePrefix,
  hasTimezonePrefix,
  parseCron,
  toCron,
} from "./cron-mapping";
import { classifyScheduleRejection } from "./validate";
import { useDescribeSchedule } from "./describe";
import {
  clampWindow,
  defaultAtTime,
  defaultEveryTime,
  displayWindow,
  isFullDay,
  timeAnchorOf,
  toggleDay,
  type EveryPattern,
  type ScheduleWindow,
} from "./transitions";

export interface ScheduleEditorProps {
  /** 当前调度配置（受控）。 */
  value: ScheduleConfig;
  /** 配置变化回调。 */
  onChange: (value: ScheduleConfig) => void;
  /** 工作区 ID（用于向服务器请求 cron 预览）。 */
  wsId: string;
  /** 是否整体禁用（如无权限）。 */
  disabled?: boolean;
  /** 禁用时展示的原因文案。 */
  disabledReason?: string;
  /** 服务器接受/拒绝当前表达式时触发，让宿主弹窗的提交按钮与行内报错保持同步。 */
  onValidityChange?: (valid: boolean) => void;
}

// 预览请求的防抖时间：停止输入这么久后才真正向服务器问一次"这表达式会何时跑"。
const PREVIEW_DEBOUNCE_MS = 300;

// 定时器：每 intervalMs 毫秒刷新一次"当前时间"，驱动倒计时展示。
function useNowTicker(intervalMs = 30_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

// 倒计时文案：按剩余时间量级选用 "<1 分钟" / "X 分" / "X 时 Y 分" / "X 天 Y 时 Z 分"。
function useFormatCountdown() {
  const { t } = useT("autopilots");
  return (target: Date, now: Date): string => {
    const diffMs = target.getTime() - now.getTime();
    if (diffMs < 60_000) return t(($) => $.schedule_editor.countdown.less_than_minute);
    const minutes = Math.floor(diffMs / 60_000);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);
    if (days > 0)
      return t(($) => $.schedule_editor.countdown.days_hours, {
        days,
        hours: hours % 24,
        minutes: minutes % 60,
      });
    if (hours > 0)
      return t(($) => $.schedule_editor.countdown.hours_minutes, {
        hours,
        minutes: minutes % 60,
      });
    return t(($) => $.schedule_editor.countdown.minutes, { minutes });
  };
}

/** 计划的一个维度区块。用 fieldset 而非样式化 div：键盘用户仍能到达的置灰控件会
 *  静默覆盖已存表达式——fieldset 的 disabled 才真正阻止这一切。 */
function ScheduleField({
  label,
  disabled,
  children,
}: {
  label: string;
  disabled: boolean;
  children: ReactNode;
}) {
  return (
    // 这里不能有 `m-0`：它会与父容器 space-y 的优先级打平并靠源码顺序胜出，把区块
    // 间的间距清零。Preflight 已重置 fieldset 的外边距。
    //
    // 标签在控件上方、每块占满整行：面板是窄的侧栏列，标签列会把整整四分之一
    // 宽度从要塞下一整段时间区间的控件上抢走。
    // 标签到控件 6px，对照一块自身控件间 8px、块间 20px——所以标签读作其下内容的
    // 小标题。
    <fieldset
      disabled={disabled}
      className={cn("flex min-w-0 flex-col gap-1.5 border-0 p-0", disabled && "opacity-60")}
    >
      <p className="text-caption font-medium text-muted-foreground">{label}</p>
      {/* 用 flex gap 而非 space-y：Base UI 的 Select 会在触发器后追加一个隐藏的
          固定定位表单输入，space-y 会把它当最后一个子元素——把它的 8px 让给可见
          控件、撑大区块。 */}
      <div className="flex min-w-0 flex-col gap-2">{children}</div>
    </fieldset>
  );
}

/** 数字输入框：DOM 文本与模型值绝不漂移——输入中允许清空，失焦时把框与模型对账，
 *  超界数字夹紧回字段边界，框内没有数字时弹回已提交值。没有夹紧的话，在 1–23 的
 *  字段里敲 "24" 会先提交首键的 2、随后拒绝整对，留下一个用户从没选过的
 *  "每 2 小时"计划。 */
function NumberField({
  value,
  min,
  max,
  onCommit,
  className,
  ariaLabel,
  autoFocus,
  // 本框所在的外壳。独立出现时自带边框与焦点环；嵌在 InputGroup 里时，边框、环
  // 与焦点槽归组所有，InputGroupInput 就是把三者都让出去的同一字段。
  component: Field = Input,
}: {
  value: number;
  min: number;
  max: number;
  onCommit: (value: number) => void;
  className?: string;
  ariaLabel: string;
  autoFocus?: boolean;
  component?: ComponentType<ComponentProps<"input">>;
}) {
  const [text, setText] = useState(String(value));
  const lastValueRef = useRef(value);
  if (lastValueRef.current !== value) {
    lastValueRef.current = value;
    setText(String(value));
  }
  return (
    <Field
      type="number"
      aria-label={ariaLabel}
      // 由调用方门控：只在用户刚揭开该字段时设置，绝不在首次挂载时。
      autoFocus={autoFocus}
      min={min}
      max={max}
      value={text}
      onChange={(e) => {
        setText(e.target.value);
        const n = parseInt(e.target.value, 10);
        if (!Number.isNaN(n) && n >= min && n <= max) onCommit(n);
      }}
      // 原生步进器在边界死停；本面板里其他字段都会回绕，所以这里也回绕。从 31
      // 步出给出 1 号，而不是再来一次没反应的按键。
      onKeyDown={(e) => {
        if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
        e.preventDefault();
        const typed = parseInt(text, 10);
        // 越界框不是可"步出"的起点——步进正是把它带回来的手段，且落在它越过的
        // 那个边界上。先夹紧再步进则会从那边界步出去，边界本身永远到不了：1-31
        // 字段里对敲出的 "0" 按方向键给出 2，对 "40" 给出 30。
        const stepped =
          Number.isNaN(typed) || typed >= min && typed <= max
            ? (Number.isNaN(typed) ? value : typed) + (e.key === "ArrowUp" ? 1 : -1)
            : Math.min(max, Math.max(min, typed));
        const wrapped = stepped > max ? min : stepped < min ? max : stepped;
        setText(String(wrapped));
        if (wrapped !== lastValueRef.current) onCommit(wrapped);
      }}
      onBlur={() => {
        const n = parseInt(text, 10);
        if (Number.isNaN(n)) {
          setText(String(lastValueRef.current));
          return;
        }
        const clamped = Math.min(max, Math.max(min, n));
        setText(String(clamped));
        if (clamped !== lastValueRef.current) onCommit(clamped);
      }}
      className={className}
    />
  );
}

// 两个窗口字段贴在一起，让旁边的间隔框保住宽度。从字段默认内边距和数字间隙里
// 裁减：组自己的边框已框住它们，两端之间只有 "~"，所以宽松的独立间距是本行
// 匀不出来的宽度。
const WINDOW_FIELD_COMPACT = "gap-0.5 px-1";

/**
 * 定时器编辑器。分区块编辑"时间/日期/时区"，底部统一展示 cron 文本、白话回读
 * 与服务器预览。结构化字段每次变更都写回模型、实时生成 cron；直接编辑 cron 文本
 * 会尝试解析回结构化（解析不动就进入仅高级模式）。合法性以服务器对当前表达式的
 * 裁决为准，通过 onValidityChange 通知宿主提交闸。
 * @param value 当前配置。
 * @param onChange 配置变化回调。
 * @param wsId 工作区 ID。
 * @param disabled 是否禁用。
 * @param disabledReason 禁用原因。
 * @param onValidityChange 合法性变化回调。
 */
export function ScheduleEditor({
  value,
  onChange,
  wsId,
  disabled,
  disabledReason,
  onValidityChange,
}: ScheduleEditorProps) {
  const { t, i18n } = useT("autopilots");
  const describe = useDescribeSchedule();
  const formatCountdown = useFormatCountdown();
  const now = useNowTicker();
  const advanced = value.raw !== null;
  const locked = disabled === true;

  // cron 文本在用户不动 cron 输入框时镜像结构化模型；在输入期间草稿胜出，直到它
  // 被提交。只在 blur/Enter 时应用（绝不在打字暂停时），避免敲了一半的表达式把
  // 编辑器中途翻进仅高级模式。
  const [cronDraft, setCronDraft] = useState<string | null>(null);
  // 已提交表达式的两种视图。线上形式（toCron）把时区作为 `TZ=` 前缀携带，是校验
  // 与保存所见的形式；输入框只放五个字段，因为它的前缀是取自选择器的固定组段，
  // 不是用户可编辑可删除的字符。
  const committedCron = toCron(value);
  const fieldsText = cronFields(value);
  const cronText = cronDraft ?? fieldsText;
  // 用户手打的 `TZ=` 前缀在服务器裁决前住在文本自身里（见 applyDraft），期间固定
  // 段让位——屏上两个前缀会读成一个同时携带两者的表达式。
  const ownPrefix = hasTimezonePrefix(cronText);
  const [cronEditing, setCronEditing] = useState(false);
  const cronInputRef = useRef<HTMLInputElement | null>(null);
  // 自动聚焦把光标留到末尾，长表达式一打开头就滚出视野；编辑从回读停下的地方开始。
  useEffect(() => {
    if (!cronEditing) return;
    const input = cronInputRef.current;
    if (input === null) return;
    input.setSelectionRange(0, 0);
    input.scrollLeft = 0;
  }, [cronEditing]);
  const cronErrorId = useId();
  // 仅高级表达式没有别处可编辑，所以它的字段常开。
  const cronOpen = advanced || cronEditing;

  // 记忆锚点：切到"每 N"再切回"固定时刻"时，时间从用户上一次表达过的 时:分 恢复。
  const timeAnchorRef = useRef(timeAnchorOf(value.time) ?? "09:00");
  const currentAnchor = timeAnchorOf(value.time);
  if (currentAnchor !== null) timeAnchorRef.current = currentAnchor;

  const everyAnchorRef = useRef<EveryPattern | null>(null);
  if (value.time.kind === "every") everyAnchorRef.current = value.time;

  // 小时步长上限 23、分钟 59，切换单位时会夹紧——45 分钟的步长扫一眼"小时"回来
  // 会变成 23。每个单位记住自己最后一次的步长。
  const intervalAnchorRef = useRef<Record<EveryPattern["unit"], number | null>>({
    hours: null,
    minutes: null,
  });
  if (value.time.kind === "every") {
    intervalAnchorRef.current[value.time.unit] = value.time.interval;
  }

  // 日期种类也互相装不下对方的值："每天"没有星期集合、每周没有日号。否则扫一眼
  // 别的种类再回来，会看到周二/周四/周六变成周一、25 号变成 1 号。
  const daysOfWeekAnchorRef = useRef<number[]>([1]);
  const dayOfMonthAnchorRef = useRef(1);
  if (value.days.kind === "weekly") daysOfWeekAnchorRef.current = value.days.daysOfWeek;
  if (value.days.kind === "monthly") dayOfMonthAnchorRef.current = value.days.dayOfMonth;

  // 聚焦模式切换刚揭开的字段，用户能直接开打：时间切换换上一个全新控件、"每月"
  // 会带出日号框。标志在切换时置位、由挂载控件的 autoFocus 读取、提交后清除——
  // 因此只响应用户切换，绝不响应弹窗首帧渲染（那会抢焦点并在打开时滚动面板）。
  // 一个被动 effect 在 autoFocus 早已跑完的提交之后重置它。
  const focusTimeFieldRef = useRef(false);
  const focusDayFieldRef = useRef(false);
  useEffect(() => {
    focusTimeFieldRef.current = false;
    focusDayFieldRef.current = false;
  });

  // 手打的、携带 `TZ=` 前缀、等待服务器裁决后才被提升成"已拆出形态"的表达式
  // （见 applyDraft）。每次使用前与 committedCron 比对，之后的编辑会淘汰它而不必
  // 显式清除。
  const pendingTzPromotionRef = useRef<string | null>(null);

  /** 提交 cron 框的文本。返回编辑器事后是否处于仅高级模式——调用方无法从
   *  `advanced` 读到它，那还是本次渲染刚要离开的值。 */
  const applyDraft = (draft: string): boolean => {
    const next = draft.trim();
    setCronDraft(null);
    // 空表达式不是计划：弹回模型，而不是把 "" 写进 `raw` 让弹窗提交它。
    if (next.length === 0 || next === cronFields(value)) return advanced;
    // 手打的 `TZ=` 前缀不在此处拆出。拆出会同时重写两个控件——时区落进选择器、
    // 文本丢掉前缀——而只有服务器能说时区是真的，所以两次重写都等它的裁决：表达式
    // 原样提交（高级），下面的 effect 在"这份精确文本的预览"被接受时把它提升成
    // 已拆出的一对。在那之前选择器绝不显示一个服务器可能拒绝的时区，框也绝不丢
    // 掉用户可能仍在修改的字符。（水合不同：parseCron 立即拆出，因为已存储的表达
    // 式保存时就过过服务器校验。）
    if (extractTimezonePrefix(next) !== null) {
      pendingTzPromotionRef.current = next;
      onChange({ ...value, raw: next });
      return true;
    }
    pendingTzPromotionRef.current = null;
    const parsed = parseCron(next, value.timezone);
    // 超出模型的表达式让结构化计划原样垫在下面。parseCron 没有旧配置可用、会带回
    // 09:00 默认值 + `raw` 文本；让那些默认值落地会把置灰控件显示成用户从没设过的
    // 计划，并在他们再次简化表达式时丢掉真正的计划。
    onChange(parsed.raw === null ? parsed : { ...value, raw: next });
    return parsed.raw !== null;
  };

  const setTime = (time: TimePattern) => {
    setCronDraft(null);
    onChange({ ...value, time, raw: null });
  };
  const setDays = (days: DayPattern) => {
    setCronDraft(null);
    onChange({ ...value, days, raw: null });
  };
  // 时区与表达式正交，所以即便在仅高级模式下它也可编辑——`raw` 保持不动。
  const setTimezone = (timezone: string) => onChange({ ...value, timezone });

  // 预览在编辑器锁定时也保持开启：那是用户仍需要的只读信息（旧编辑器也显示它）。
  // 它跟随已提交表达式而非草稿——校验在 cron 字段离开时进行一次，绝不打字一次。
  const previewExpr = useDebouncedValue(committedCron, PREVIEW_DEBOUNCE_MS);
  const preview = useQuery(
    cronPreviewOptions(wsId, previewExpr, value.timezone, {
      enabled: previewExpr.trim().length > 0,
    }),
  );
  const { refetch } = preview;
  // `null` 表示响应读不出来（schema 漂移）——区别于空列表（表达式确实从不触发）。
  const nextRuns = preview.data?.next_runs ?? null;

  // 只有 400 才表示"这个输入非法"——传输/服务器故障不能被画成用户 cron 写错。
  // 服务器会标注它拒绝了哪个输入，因为 cron 与时区固定在不同控件里。
  const previewIsCurrent = previewExpr === committedCron;
  const liveRejection =
    previewIsCurrent && preview.error instanceof ApiError && preview.error.status === 400
      ? preview.error
      : null;

  // 服务器一旦接受，就把待定 `TZ=` 表达式提升——且只提升裁决针对的那份精确文本。
  // 预览判定的是带前缀的原样表达式（服务器自己读内嵌时区），所以 200 同时覆盖
  // 提升所做的两次重写：时区移进选择器、前缀从 cron 框取下。被拒绝则什么都不发生：
  // 文本保持手打原样，躺在服务器自己的报错下面。
  const promoted = pendingTzPromotionRef.current;
  useEffect(() => {
    if (promoted === null || promoted !== committedCron) return;
    if (!previewIsCurrent || !preview.isSuccess) return;
    pendingTzPromotionRef.current = null;
    onChange(parseCron(promoted, value.timezone));
  }, [promoted, committedCron, previewIsCurrent, preview.isSuccess, value.timezone, onChange]);
  // 服务器最后那次裁决比产生它的请求活得更久，但只对它裁定的精确输入有效——表达式
  // 与时区两者，因为服务器拒绝任一。该输入被重新查询时（重试、断网后 refetch）查询
  // 既不持错误也不持数据，缺了这一层，被拒会在眼前闪灭、Save 会对一份服务器已拒绝
  // 的 cron 恢复可用——而写入前检查也抓不到它，因为网络失败时它会把计划放行。只有
  // 可读的答案才改变裁决；传输故障对 cron 不置一词，任一输入变化都会让旧裁决因不再
  // 匹配键而退休。
  //
  // 本组件有两个键，相距一次防抖。`committedKey` 是用户已落定的输入；`queriedKey`
  // （下面）才是查询——以及它的 `refetch` 与手上的数据——真正属于的输入。凡推
  // 理"用户输入"用已提交键；凡推理"那次请求"用已查询键。
  const committedKey = `${committedCron} ${value.timezone}`;
  const verdictRef = useRef<{
    key: string;
    rejection: ApiError | null;
    accepted: boolean;
  } | null>(null);
  if (previewIsCurrent) {
    if (liveRejection !== null) {
      verdictRef.current = { key: committedKey, rejection: liveRejection, accepted: false };
    } else if (preview.isSuccess) {
      // `accepted` 许可的是高级提示："服务器接受此表达式"，不是"尚未拒绝它"。
      // 不可读的 200（schema 漂移）什么都不接受。
      verdictRef.current = { key: committedKey, rejection: null, accepted: nextRuns !== null };
    }
  }
  const verdict = verdictRef.current?.key === committedKey ? verdictRef.current : null;
  const rejection = liveRejection ?? verdict?.rejection ?? null;
  const scheduleRejection = rejection !== null ? classifyScheduleRejection(rejection) : null;
  const cronErrorDetail = scheduleRejection?.detail ?? null;
  const previewUnavailable =
    previewIsCurrent &&
    ((preview.error !== null && cronErrorDetail === null) ||
      (preview.isSuccess && nextRuns === null));

  // 预览每次编辑后晚一个往返才到。飞行中卸载该行会让它每敲一个键闪灭重来，所以
  // 最后答案留在屏上、文本就地替换。它变暗并标忙碌直到它属于当前表达式，过期列表
  // 绝不被当作这份计划的答案。
  const previewIsSettled = previewIsCurrent && preview.isSuccess && nextRuns !== null;
  const serverAccepted = verdict?.accepted === true;
  const shownPreviewRef = useRef<{ runs: string[]; timezone: string } | null>(null);
  if (
    previewIsSettled &&
    (shownPreviewRef.current?.runs !== nextRuns ||
      shownPreviewRef.current.timezone !== value.timezone)
  ) {
    shownPreviewRef.current = { runs: nextRuns, timezone: value.timezone };
  }
  const shownPreview = shownPreviewRef.current;
  // 每份预览只格式化一次，而非每渲染一次：cron 框里每个字符与每 30 秒倒计时 tick
  // 都会重渲编辑器，而这些 Intl 格式只在预览或 locale 变化时才变。每次运行完整渲染
  // ——"Jul 13, 3:00 PM"、"7月13日 15:00"——因为带日期的行下的裸时间读起来像它
  // 的脚注。
  const shownRuns = useMemo(() => {
    if (shownPreview === null) return [];
    return shownPreview.runs.map((iso) => ({
      iso,
      label: formatInTimeZone(iso, shownPreview.timezone, i18n.language),
      at: Date.parse(iso),
    }));
  }, [shownPreview, i18n.language]);
  const previewIsPending = !previewIsSettled;
  // 忙碌/变暗只对列表有意义：不可用提示永不落定，永远变暗会读成永久加载态。
  const previewShowsList =
    cronErrorDetail === null &&
    !previewUnavailable &&
    shownPreview !== null &&
    shownPreview.runs.length > 0;

  // 畸形时间戳必须降级为"无倒计时"，绝不变成 NaN，所以每个值先查再变成 Date
  //（见下方列表）。
  const firstRunMs = nextRuns?.[0] !== undefined ? Date.parse(nextRuns[0]) : Number.NaN;

  // 预览有缓存，所以一旦它的首条进入过去，整份列表就过期了——刷新它。守卫同时
  // 命名输入与它为之而发的运行：没有运行，比服务器快的客户端时钟会每个 tick 都
  // refetch；没有输入，两个共享同一 next-run 时刻的计划（任何两个在明天 09:00 触发
  // 的都是）会把一个的刷新当成覆盖另一个，再没有别的能救它——预览新鲜 30 秒，而
  // refetchOnWindowFocus 全仓库关着。输入用*防抖后*表达式而非已提交表达式：查询、
  // `refetch` 与 `firstRunMs` 都属于前者，命名已提交的那个会把新表达式标记为已刷新、
  // 而手上的数据还是上一个表达式的——把它应得的刷新在预览抵达前吞掉。
  const queriedKey = `${previewExpr} ${value.timezone}`;
  const refetchedForRef = useRef<string | null>(null);
  useEffect(() => {
    if (Number.isNaN(firstRunMs) || firstRunMs > now.getTime()) return;
    const fetchedFor = `${queriedKey} ${firstRunMs}`;
    if (refetchedForRef.current === fetchedFor) return;
    refetchedForRef.current = fetchedFor;
    void refetch();
  }, [firstRunMs, now, refetch, queriedKey]);

  // 弹窗拥有提交按钮，所以它得知道服务器拒绝的表达式——否则 Save 会持久化一份
  // 永不运行的 cron。表达式与时区各是各的依赖：一个乐观清掉自己提交时拒绝的弹窗，
  // 需要裁决被重述——即便拒绝文本还是它已有的那句（落到这个编辑的查询的缓存 400
  // 到达时中间没有 null）。
  useEffect(() => {
    onValidityChange?.(cronErrorDetail === null);
  }, [cronErrorDetail, committedCron, value.timezone, onValidityChange]);

  const timezones = useMemo(() => timezoneOptions(value.timezone), [value.timezone]);

  const description = describe(value);

  const dayKindLabel = (kind: DayPattern["kind"]): string => {
    if (kind === "every") return t(($) => $.schedule_editor.days_every);
    if (kind === "weekly") return t(($) => $.schedule_editor.days_weekly);
    return t(($) => $.schedule_editor.days_monthly);
  };
  const dayKindItems = (["every", "weekly", "monthly"] as const).map((kind) => ({
    value: kind,
    label: dayKindLabel(kind),
  }));

  return (
    // 块间 20px，块内 8px（标签→控件、控件→自己的附加行）：一块与邻居的距离必须
    // 大于与自身部件的距离，否则整个面板坍缩成一面控件墙。
    <div className="space-y-5">
      <ScheduleField label={t(($) => $.schedule_editor.time_label)} disabled={locked || advanced}>
        <SegmentedToggle
          value={value.time.kind}
          options={[
            ["at", t(($) => $.schedule_editor.time_at)],
            ["every", t(($) => $.schedule_editor.time_every)],
          ]}
          onChange={(kind) => {
            focusTimeFieldRef.current = true;
            setTime(
              kind === "at"
                ? {
                    kind: "at",
                    time: defaultAtTime(value.time, timeAnchorRef.current),
                  }
                : defaultEveryTime(value.time, everyAnchorRef.current),
            );
          }}
        />
        {value.time.kind === "at" ? (
          <TimeInput
            value={value.time.time}
            hourLabel={t(($) => $.schedule_editor.a11y.fixed_hour)}
            minuteLabel={t(($) => $.schedule_editor.a11y.fixed_minute)}
            onChange={(v) => setTime({ kind: "at", time: v })}
            autoFocus={focusTimeFieldRef.current}
          />
        ) : (
          <EveryTimeControls
            time={value.time}
            intervalAnchor={intervalAnchorRef.current}
            onSet={setTime}
            autoFocus={focusTimeFieldRef.current}
          />
        )}
      </ScheduleField>

      <ScheduleField label={t(($) => $.schedule_editor.days_label)} disabled={locked || advanced}>
        {/* 7:4 网格，与上面间隔行的模板一致，让两行在按分钟单位时对齐成同一个双列
            网格。网格轨道只按模板取宽——不像 flex 会让每个框自身的 padding 与
            w-fit/w-full 扭曲切分、把两列顶出对齐。没有日号框时 select 横跨两轨，
            让每天/每周仍填满整行。 */}
        <div className="grid grid-cols-[7fr_4fr] items-center gap-2">
          <Select
            items={dayKindItems}
            value={value.days.kind}
            onValueChange={(v) => {
              if (!v || v === value.days.kind) return;
              if (v === "every") setDays({ kind: "every" });
              else if (v === "weekly")
                setDays({ kind: "weekly", daysOfWeek: daysOfWeekAnchorRef.current });
              else {
                // 只有"每月"会揭开一个待输入字段；日号框拿到光标，日号无需第二次点击。
                focusDayFieldRef.current = true;
                setDays({ kind: "monthly", dayOfMonth: dayOfMonthAnchorRef.current });
              }
            }}
          >
            <SelectTrigger
              aria-label={t(($) => $.schedule_editor.a11y.day_pattern)}
              className={cn("w-full min-w-0", value.days.kind !== "monthly" && "col-span-2")}
            >
              <SelectValue>{dayKindLabel(value.days.kind)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {dayKindItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {value.days.kind === "monthly" && (
            <NumberField
              value={value.days.dayOfMonth}
              min={1}
              max={31}
              ariaLabel={t(($) => $.schedule_editor.a11y.day_of_month)}
              onCommit={(n) => setDays({ kind: "monthly", dayOfMonth: n })}
              className="h-8 min-w-0"
              autoFocus={focusDayFieldRef.current}
            />
          )}
        </div>
        {value.days.kind === "weekly" && (
          <div className="flex w-full gap-0.5">
            {DAY_KEYS.map((dayKey, i) => {
              const days = value.days;
              const selected = days.kind === "weekly" && days.daysOfWeek.includes(i);
              return (
                <button
                  key={dayKey}
                  type="button"
                  aria-pressed={selected}
                  // chip 自己的文本是缩写；全称不是——所以 aria-label 用全称。
                  aria-label={t(($) => $.schedule_editor.a11y.days_full[dayKey])}
                  onClick={() => {
                    if (days.kind !== "weekly") return;
                    setDays({
                      kind: "weekly",
                      daysOfWeek: toggleDay(days.daysOfWeek, i),
                    });
                  }}
                  // flex-1 + min-w-0：七个 chip 均分行宽，两种状态共用一个盒模型，
                  // 标签宽度变化不会改变某 chip 的尺寸。
                  className={cn(
                    "inline-flex h-6.5 min-w-0 flex-1 items-center justify-center rounded-md",
                    "text-micro font-medium leading-none transition-colors",
                    selected
                      ? "bg-foreground text-background"
                      : "bg-muted text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t(($) => $.schedule_editor.days_short[dayKey])}
                </button>
              );
            })}
          </div>
        )}
        {value.days.kind === "monthly" && value.days.dayOfMonth >= 29 && (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.schedule_editor.monthly_short_month_hint, {
              day: value.days.dayOfMonth,
            })}
          </p>
        )}
      </ScheduleField>

      <ScheduleField label={t(($) => $.schedule_editor.timezone_label)} disabled={locked}>
        {/* fieldset 已禁用触发器；disabled:opacity-100 防止控件在它之上二次变暗。 */}
        <TimezonePicker
          value={value.timezone}
          onChange={setTimezone}
          options={timezones}
          ariaLabel={t(($) => $.schedule_editor.a11y.timezone)}
          className="disabled:opacity-100"
        />
      </ScheduleField>

      {/* cron 文本、白话回读与服务器预览都是同一表达式的派生视图，共用一块面板：
          它是上面字段的结果，而非与它们并排的更多字段。面板内的 cron 行兼任高级
          编辑入口——点它把文本换成输入框，raw 模式期间表单整体变暗。 */}
      <div className="rounded-md bg-muted/40 p-2.5 text-caption text-muted-foreground">
        <div className="space-y-2">
          {/* 白话句子领衔：它是人读的那一行，占面板入口与前景色；表达式降为它下方
              的技术回声。 */}
          {description !== null && (
            <p className="flex items-start gap-1.5 text-foreground">
              <Clock className="mt-0.5 h-3 w-3 shrink-0" />
              <span>{description}</span>
            </p>
          )}
          {cronOpen ? (
            // `TZ=` 段属于表达式，但不属于可编辑文本：它渲染成取自时区选择器的
            // 固定 addon——时区不可能被删去一半变成 robfig 会 panic 的前缀，且字段
            // 原样流向服务器。
            <InputGroup className="bg-background dark:bg-input/30">
              {!ownPrefix && (
                // 字段上方单独一行，镜像闭合态回读：长时区名与长字段各自截断，
                // 而不是把一行在中间劈开。
                <InputGroupAddon align="block-start" className="font-mono text-caption">
                  {/* eslint-disable-next-line i18next/no-literal-string -- cron syntax, not copy */}
                  <span className="min-w-0 truncate">TZ={value.timezone}</span>
                </InputGroupAddon>
              )}
              <InputGroupInput
                ref={cronInputRef}
                type="text"
                // 只在用户主动打开字段时聚焦。仅高级表达式首帧就开着，那里自动聚焦
                // 会把焦点（和弹窗滚动）拉到面板底、越过用户来编辑的标题。
                autoFocus={cronEditing}
                aria-label={t(($) => $.schedule_editor.cron_toggle)}
                value={cronText}
                disabled={locked}
                onChange={(e) => setCronDraft(e.target.value)}
                onBlur={() => {
                  // 看草稿落地后的模式，而不是正在离开的那个：表达式简化回模型必须
                  // 关闭字段，而 `advanced` 在本渲染中仍是 true。
                  const stillAdvanced = cronDraft !== null ? applyDraft(cronDraft) : advanced;
                  // 仅高级表达式没有结构化视图可退回，字段保持打开。
                  if (!stillAdvanced) setCronEditing(false);
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && cronDraft !== null) {
                    e.preventDefault();
                    // 应用可能结束仅高级模式，那会让字段塌回回读、焦点掉到用户手下的
                    // body。字段保持打开直到他们离开它。
                    setCronEditing(true);
                    applyDraft(cronDraft);
                  }
                }}
                aria-invalid={cronErrorDetail !== null}
                aria-describedby={cronErrorDetail !== null ? cronErrorId : undefined}
                className="font-mono text-body"
              />
            </InputGroup>
          ) : (
            // 表达式就地编辑：点它把文本换成输入框，而不是在下方再开一份副本。
            <button
              type="button"
              disabled={locked}
              onClick={() => setCronEditing(true)}
              // 铅笔是装饰；这是读屏软件听到的名字。
              aria-label={t(($) => $.schedule_editor.cron_click_to_edit)}
              className="flex w-full items-center gap-1.5 text-left hover:text-foreground disabled:pointer-events-none disabled:opacity-60"
            >
              {/* 闭合时回读只显示可编辑文本——时区已在上面选择器里上屏，而自前缀的
                  高级表达式把时区装在文本自身。完整线上形态在字段打开时出现，作为
                  字段上方的固定段。 */}
              <span className="min-w-0 truncate font-mono">{cronText}</span>
              <Pencil aria-hidden className="h-3 w-3 shrink-0 opacity-60" />
            </button>
          )}
          {/* 表达式下方只放一条注释，绝不叠成一摞：拒绝是用户必须行动的、高级提示
              解释控件为何关闭、语法提示是兜底。 */}
          {cronErrorDetail !== null ? (
            // 拒绝点名它所属的字段（aria-describedby）并自我宣告：没有它，读屏
            // 用户对坏表达式得到的唯一反馈就是一个沉默的 Save 按钮。
            <div id={cronErrorId} role="alert" className="space-y-0.5">
              <p className="text-destructive">
                {scheduleRejection?.code === "invalid_timezone"
                  ? t(($) => $.schedule_editor.timezone_invalid)
                  : t(($) => $.schedule_editor.cron_invalid)}
              </p>
              {/* 解析器自己的原话，一字不改——不翻译，但它是唯一说明哪个字段出错的
                  文本。 */}
              <p className="font-mono text-micro text-destructive">{cronErrorDetail}</p>
            </div>
          ) : advanced ? (
            // 这里说的是三件不同的事，只有第一件是关于表达式的陈述：服务器收下它
            // 而控件显示不了；我们问不到；我们还没问。
            <p>
              {serverAccepted
                ? t(($) => $.schedule_editor.advanced_hint)
                : previewUnavailable
                  ? t(($) => $.schedule_editor.advanced_unverified)
                  : t(($) => $.schedule_editor.advanced_checking)}
            </p>
          ) : cronOpen ? (
            <p>{t(($) => $.schedule_editor.cron_hint)}</p>
          ) : null}
        </div>

        {/* 被拒的表达式没有可预览的东西：整节连同标题与分隔线一起消失，而不是在
            报错下留一个空框——报错已填满它腾出的空间。
            否则高度是预留的：预览落后于每次编辑一个往返，忽隐忽现的区块会让弹窗
            重排。多次拉取间元素相同：React 替换文本节点而非拆掉整节，所以同一计划
            的重渲染绝不闪动——只变暗并报 busy，直到答案是当前的。 */}
        {cronErrorDetail === null && (
        <div
          aria-busy={previewShowsList ? previewIsPending : undefined}
          className={cn(
            "mt-2.5 min-h-14 border-t border-border/60 pt-2.5 transition-opacity",
            previewShowsList && previewIsPending && "opacity-50",
          )}
        >
          <p className="mb-1.5 font-medium text-foreground">
            {t(($) => $.schedule_editor.next_runs_label)}
          </p>
          {previewUnavailable ? (
            <p>{t(($) => $.schedule_editor.preview_unavailable)}</p>
          ) : shownPreview !== null && shownPreview.runs.length > 0 ? (
            // 用网格而非每行 flex：本地化日期不是固定宽度，只有共享列才能让倒计时
            // 彼此对齐。max-content 把该列撑到最宽日期的宽度。
            <ul className="grid grid-cols-[max-content_max-content] gap-x-5 gap-y-1">
              {shownRuns.map(({ iso, label, at }) => (
                // contents：一行的两个单元格属于上面的网格，不属于自己那格的盒子。
                <li key={iso} className="contents">
                  {/* 日期深色、倒计时弱化：图标列没有了——网格已把行对齐，描述行的
                      时钟保持为面板唯一图标。 */}
                  <span className="text-foreground tabular-nums">{label}</span>
                  {/* 每次运行自带倒计时，贴着它倒数的时刻。 */}
                  <span className="whitespace-nowrap tabular-nums opacity-70">
                    {Number.isNaN(at)
                      ? ""
                      : t(($) => $.schedule_editor.next_in, {
                          countdown: formatCountdown(new Date(at), now),
                        })}
                  </span>
                </li>
              ))}
            </ul>
          ) : previewIsSettled ? (
            // 可读响应里的空列表：语法合法，但从不触发。
            <p>{t(($) => $.schedule_editor.no_upcoming_runs)}</p>
          ) : null}
        </div>
        )}
      </div>
      {disabled === true && disabledReason !== undefined && (
        <p className="mt-2 text-micro text-muted-foreground">{disabledReason}</p>
      )}
    </div>
  );
}

// "每 N"时间块：步长数字 + 单位选择 + 运行窗口（起点 ~ 终点），带各自锚点记忆。
function EveryTimeControls({
  time,
  intervalAnchor,
  onSet,
  autoFocus,
}: {
  time: EveryPattern;
  intervalAnchor: Record<EveryPattern["unit"], number | null>;
  onSet: (time: TimePattern) => void;
  autoFocus?: boolean;
}) {
  const { t } = useT("autopilots");
  const maxInterval = time.unit === "hours" ? 23 : 59;
  const unitItems = (["hours", "minutes"] as const).map((unit) => ({
    value: unit,
    label:
      unit === "hours"
        ? t(($) => $.schedule_editor.unit_hours)
        : t(($) => $.schedule_editor.unit_minutes),
  }));
  // 屏上显示的就是两个字段的边界。`time.window` 是计划持有的——全天时为 null——
  // 而无论哪种两个字段都有东西显示，所以用户通过编辑某一天的边界来收窄窗口，而不
  // 必先声明"我想要一个窗口"。
  const window = displayWindow(time);

  // 单位切换时聚焦步长字段，用户可不用把手伸回去、直接为新单位重打。字段跨单位
  // 切换保持挂载，所以 autoFocus（仅挂载）做不到——在切换完成重渲染后再聚焦它。
  // 从 group 上 query 而非经 NumberField/Input 传 ref：那些共享组件不转发 ref，
  // 而步长是组内唯一的数字输入。
  const groupRef = useRef<HTMLDivElement>(null);
  const focusStepOnUnitChangeRef = useRef(false);
  useEffect(() => {
    if (!focusStepOnUnitChangeRef.current) return;
    focusStepOnUnitChangeRef.current = false;
    const step = groupRef.current?.querySelector<HTMLInputElement>('input[type="number"]');
    step?.focus();
    step?.select();
  }, [time.unit]);

  // 用户真正要的终点。窗口不能倒着走，所以起点推过终点会把终点一起拖走——但起点
  // 收回时终点必须回来，否则"09:00–15:00"被误打成 22:00 起点会存成"09:00–22:00"、
  // autopilot 跑得比设定频繁一倍。
  const endAnchorRef = useRef<number | null>(null);
  // 本控件自己因拖动起点而产生的终点小时。其余一切终点——敲进终点字段或 cron 框
  // 的、从服务器水合的、手动放宽的——都是用户的新意图、重新播种锚点；只有拖动会让
  // 它留在原地，因为拖动正是这个锚要撤销的对象。
  //
  // 记小时而非 "HH:MM" 文本：单位切换会把 22:00 重写成 22:59 而用户什么都没碰，
  // 把那当作新意图会把拖开的窗口当作他们要求的交还回去。
  const draggedEndRef = useRef<number | null>(null);
  if (timeParts(window.to).hour !== draggedEndRef.current) {
    endAnchorRef.current = timeParts(window.to).hour;
  }
  // 起点的编辑会带来终点的跟着动：终点小时 = max(记忆锚点, 起点小时)。
  const anchoredEnd = (from: string, to: string): string => {
    const end = Math.max(endAnchorRef.current ?? timeParts(to).hour, timeParts(from).hour);
    return `${pad2(end)}:${pad2(timeParts(to).minute)}`;
  };

  // 两个字段的每次编辑都落到这里，横跨一天的窗口被提交为"没有窗口"——无论用户从
  // 哪条路抵达，全天运行的计划都只有一种形态。
  const setWindow = (next: ScheduleWindow, minute: number) => {
    const clamped = clampWindow(next, time.unit, minute);
    onSet({ ...time, minute, window: isFullDay(clamped) ? null : clamped });
  };

  return (
    // 频率与窗口在一行上："每 N 小时"是多久一次，"from~to"是它跑在其中的每日区间
    // ——两个设置、两个框、并排。7:4 网格，与下面日期行的模板一致，两行在按分钟
    // 单位时读成一个双列网格。窗口的 min-w-fit 让它的轨道可超出 4 份追到内容宽，
    // 按小时单位时更宽的 HH:MM ~ HH:MM 才放得下（间隔轨道让位）而不被裁剪——那个
    // 单位于是走出对齐，而对齐只对分钟单位有要求。
    <div ref={groupRef} className="grid min-w-0 grid-cols-[7fr_4fr] items-center gap-2">
      {/* "每 3 小时"是一个设置，所以读作一个控件：前缀、步长与单位共用一个盒子。
          步长取走另外两个剩下的空间——前缀同它的翻译一样宽、单位同它最长选项一样
          宽——字段永远不必对着看不见的文本定尺寸。 */}
      <InputGroup className="min-w-0">
        {/* pl-2.5 而非 addon 默认 pl-2：这一行紧贴在日期 select 上方，两行只在
            首字符同一 x 时读作一列。Select 触发器补到 2.5。 */}
        <InputGroupAddon className="pl-2.5">
          {t(($) => $.schedule_editor.every_prefix)}
        </InputGroupAddon>
        <NumberField
          component={InputGroupInput}
          autoFocus={autoFocus}
          value={time.interval}
          min={1}
          max={maxInterval}
          ariaLabel={t(($) => $.schedule_editor.a11y.interval)}
          onCommit={(n) => onSet({ ...time, interval: n })}
          // spinner 会坐落在数字与单位之间，读作覆盖旁边那个词的一个控件。方向键
          // 仍能步进、仍会回绕——spinner 从来不会。
          // min-w 下限保证 flex-1 字段在行紧时不会被挤没、裁掉数字；够宽容纳任一
          // 单位下的两位上限。
          className="min-w-[2.5ch] px-0 text-center [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
        />
        <Select
          items={unitItems}
          value={time.unit}
          onValueChange={(v) => {
            if (!v || v === time.unit) return;
            const unit = v as EveryPattern["unit"];
            const max = unit === "hours" ? 23 : 59;
            focusStepOnUnitChangeRef.current = true;
            onSet({
              ...time,
              unit,
              // 该单位上次设过的步长（若设过）：45 分钟夹成 23 小时无法避免，
              // 回去的路上留住 23 是可以做到的。
              interval: intervalAnchor[unit] ?? Math.min(time.interval, max),
              // `minute` 原样穿过：在这里清零会静默丢掉 小时→分钟→小时 往返中
              // 的触发分。反正 toCron 对按分钟模式忽略它。
              window: time.window === null ? null : clampWindow(time.window, unit, time.minute),
            });
          }}
        >
          <InputGroupSelectTrigger
            aria-label={t(($) => $.schedule_editor.a11y.interval_unit)}
            className="pr-2.5"
          >
            <SelectValue>
              {unitItems.find((item) => item.value === time.unit)?.label}
            </SelectValue>
          </InputGroupSelectTrigger>
          {/* min-w-[7rem]：低于组件 144px 默认（两个短选项不需要它）——但又不能太低，
              让 "minutes" 连同它的对勾（item 的 pr-8）没地方。 */}
          <SelectContent className="min-w-[7rem]">
            {unitItems.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </InputGroup>
      {/* 窗口常驻屏上。一个时钟统领整个盒子（像间隔行的 "每" 那样是 addon），而
          非每个字段一个：单个图标标记这是时间控件，不花两个图标在两端之间挤掉的
          宽度。
          按分钟步长的窗口是小时粒度的——cron 把步长放进分钟字段，边界本身不携带
          各自的分钟。无论哪种两端都是同一个原地字段；分钟段只是在其毫无意义的
          地方不被提供。 */}
      {/* 坐在网格 4 份的第二列、日期行日号框的下方。min-w-fit 让该轨道超出份额
          追到内容：按分钟单位时纯小时的窗口契合份额、保持对齐；按小时时更宽的
          HH:MM ~ HH:MM 把轨道推出、间隔列让位，而不是窗口被裁剪（其段永不缩）。
          关掉 has-disabled：组内任一 disabled 都会让整组自暗，而分钟段在按分钟步长
          下有意禁用——并不表示整行不可用。锁定或高级的计划仍会变灰，来自整块外的
          fieldset。 */}
      <InputGroup className="min-w-fit has-disabled:bg-transparent has-disabled:opacity-100 dark:has-disabled:bg-input/30">
        {/* 领头的时钟不可聚焦：其后字段接手焦点与组的边框。pr-0 让起点字段自己的
            内边距决定图标到数字的间距，与两端之间的间距一致。 */}
        <InputGroupAddon className="pl-2 pr-0">
          <Clock className="size-3.5 text-muted-foreground" />
        </InputGroupAddon>
        <InputGroupTimeInput
          className={WINDOW_FIELD_COMPACT}
          showIcon={false}
          hourOnly={time.unit === "minutes"}
          hourLabel={t(($) => $.schedule_editor.a11y.window_start_hour)}
          minuteLabel={t(($) => $.schedule_editor.a11y.window_start_minute)}
          value={window.from}
          onChange={(v) => {
            // 窗口两端共享一个触发分——cron 没有第二个分可放——所以编辑任一端的分
            // 都移动两端。setWindow 必须拿到刚敲的那个分而非 time.minute：两位输入
            // 中途模型还持着第一位的值，会盖掉第二位。
            const minute = time.unit === "hours" ? timeParts(v).minute : time.minute;
            const to = anchoredEnd(v, window.to);
            // 标记这一端是拖动所致，下一次渲染保留锚点而不是把拖出的值当新意图。
            // anchoredEnd 已让小时不低于起点，所以 setWindow 的钳制不会再动它。
            draggedEndRef.current = timeParts(to).hour;
            setWindow({ from: v, to }, minute);
          }}
        />
        <InputGroupText className="shrink-0">~</InputGroupText>
        {/* 窗口的终点。编辑时两端互相约束——hourMin 让终点落不到起点之下——所以
            没有 cron 形态的倒置窗口连显示都显示不了，更别说提交。边界是字段自身的
            range 而非对输出的钳制：方向键在其内回绕（低于起点小时会落在 23，而不是
            一次无反应的按键）。
            清空它把窗口开到一天之末（hourClearTo）：在窗口终点上按退格就是键盘用户
            在说"跑到天黑"。 */}
        <InputGroupTimeInput
          className={WINDOW_FIELD_COMPACT}
          showIcon={false}
          value={window.to}
          hourMin={timeParts(window.from).hour}
          hourClearTo={23}
          hourOnly={time.unit === "minutes"}
          hourLabel={t(($) => $.schedule_editor.a11y.window_end_hour)}
          minuteLabel={t(($) => $.schedule_editor.a11y.window_end_minute)}
          onChange={(v) => {
            const minute = time.unit === "hours" ? timeParts(v).minute : time.minute;
            // 手敲而非拖动——这是起点需要交还的那个终点。在这里设置而非留给重播种
            // 规则：那条规则看不出一个碰巧等于拖动产出值的终点里的意图。
            endAnchorRef.current = timeParts(v).hour;
            draggedEndRef.current = timeParts(v).hour;
            setWindow({ from: window.from, to: v }, minute);
          }}
        />
      </InputGroup>
    </div>
  );
}
