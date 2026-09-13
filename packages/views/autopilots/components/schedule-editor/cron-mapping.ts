// cron 表达式与结构化模型的互相转换。后端解析器（robfig/cron v3，5 字段）对每个
// 字段接受一套封闭语法：`*`、`?`、整数、月份/星期名、`-` 区间、`/` 步长与 `,`
// 列表。结构化编辑器只往返下面这个子集；其余一切原样留在 `raw`（仅高级模式）。

import type { DayPattern, ScheduleConfig, TimePattern } from "./model";
import { consecutiveRuns, getDefaultScheduleConfig, pad2, timeParts } from "./model";

const DOW_NAMES: Record<string, number> = {
  SUN: 0,
  MON: 1,
  TUE: 2,
  WED: 3,
  THU: 4,
  FRI: 5,
  SAT: 6,
};

const MONTH_NAMES: Record<string, number> = {
  JAN: 1,
  FEB: 2,
  MAR: 3,
  APR: 4,
  MAY: 5,
  JUN: 6,
  JUL: 7,
  AUG: 8,
  SEP: 9,
  OCT: 10,
  NOV: 11,
  DEC: 12,
};

// 一个字段的取值范围（及其命名字典，若该字段允许名字）。
interface FieldBounds {
  min: number;
  max: number;
  names?: Record<string, number>;
}

const MINUTE: FieldBounds = { min: 0, max: 59 };
const HOUR: FieldBounds = { min: 0, max: 23 };
const DOM: FieldBounds = { min: 1, max: 31 };
const MONTH: FieldBounds = { min: 1, max: 12, names: MONTH_NAMES };
const DOW: FieldBounds = { min: 0, max: 6, names: DOW_NAMES };

/** 字段的一部分，按 robfig 的 getRange 怎么读怎么切。 */
interface RangePart {
  lo: number;
  hi: number;
  step: number;
  /** 该区间写成通配（"*"，或 normalizeField 重写成的 "?"）而非逐个写出。不是
   *  robfig 的 star 位——那一位只要步长大于 1 就会被清掉：它决定 dom 与 dow 是
   *  做 AND 还是 OR，而 parseCron 在到达这里之前就已根据字段文本解决了那个问题
   *  ——只有 dow 写作 "*" 时它才把固定日构造成结构化模型。本标记承载的是文本：
   *  因为模型回显的正是文本——小时写成 "*" 是"全天"，写成 "0-23" 是一个恰好
   *  横跨全天的窗口。 */
  wildcard: boolean;
  /** 是否写了步长，无论其值。"*" 与 "*\/1" 选出同样的取值，却不是同样的文本，
   *  折叠规则就看的是这个。 */
  stepped: boolean;
  /** 区间两端都写了（"9-21"），而不是只写一个裸值。 */
  explicitRange: boolean;
}

// 字段的一个单值部分（数字或名称），越界/非法返回 null。名称大小写不敏感。
function fieldValue(s: string, bounds: FieldBounds): number | null {
  const named = bounds.names?.[s.toUpperCase()];
  if (named !== undefined) return named;
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  // 超过安全整数范围的数字会让后端的 Atoi 溢出、对它而言是语法错误；越界的则
  // 过不了它的边界检查。
  if (!Number.isSafeInteger(n) || n < bounds.min || n > bounds.max) return null;
  return n;
}

/**
 * 解析 `<range>` 或 `<range>/<step>`，其中 `<range>` 是 `*`、单个值或 `lo-hi`——
 * robfig 逐部分语法的全集，收拢在一处。各字段只在传入的边界与名字上有别，语法
 * 含义永远相同：带步长的裸值一路走到字段上限（小时上的 "9/2" 是 9-23/2）。
 *
 * 步长在此刻意不设上限，robfig 也一样：比所跨区间还宽的步长是合法的，只是恰好只
 * 选出该区间的首值（星期上的 "*\/100" 就是周日）。其模型槽放不下那么宽步长的
 * 调用方自己会拒绝——它们知道自己装得下什么；语法不知道。
 */
function parseRangePart(part: string, bounds: FieldBounds): RangePart | null {
  const [rangeStr, stepStr, ...rest] = part.split("/");
  if (rest.length > 0 || !rangeStr) return null;

  let step = 1;
  if (stepStr !== undefined) {
    if (!/^\d+$/.test(stepStr)) return null;
    step = Number(stepStr);
    if (!Number.isSafeInteger(step) || step < 1) return null;
  }
  const stepped = stepStr !== undefined;

  // normalizeField 已把所有通配区间重写成 "*"。
  if (rangeStr === "*") {
    return {
      lo: bounds.min,
      hi: bounds.max,
      step,
      wildcard: true,
      stepped,
      explicitRange: false,
    };
  }

  const [loStr, hiStr, ...restRange] = rangeStr.split("-");
  if (restRange.length > 0 || !loStr) return null;
  const lo = fieldValue(loStr, bounds);
  if (lo === null) return null;

  let hi: number;
  if (hiStr === undefined) {
    // 裸值带步长时一路走到字段上限（"9/2" 是 9-23/2），不带时就是它自己。
    hi = stepped ? bounds.max : lo;
  } else {
    const hiVal = fieldValue(hiStr, bounds);
    if (hiVal === null) return null;
    hi = hiVal;
  }
  if (hi < lo) return null;

  return { lo, hi, step, wildcard: false, stepped, explicitRange: hiStr !== undefined };
}

/** 整个字段是否是一个钉死的值——"9"，而"9-9"、"*\/100" 这类只是凑巧只选一个值
 *  的写法不算（折叠会先把它们重写成这种形态，如果模型有对应槽位）。 */
function plainValue(field: string, bounds: FieldBounds): number | null {
  const r = parseRangePart(field, bounds);
  if (r === null || r.wildcard || r.stepped || r.explicitRange) return null;
  return r.lo;
}

/** 把 dow 字段展开成升序去重的星期集合；落在可回显语法之外时返回 null。数字、
 *  名字、区间、步长与列表都可枚举——只要 robfig 接受，dow 就可结构化，包括比
 *  一周还宽的步长（"*\/100" → 周日）。 */
function expandDow(field: string): number[] | null {
  const days = new Set<number>();
  // `field` 到这儿已归一化（见 normalizeField），所以这里的空段是真·畸形表达式，
  // 不是多余的逗号。
  for (const part of field.split(",")) {
    const r = parseRangePart(part, DOW);
    if (r === null) return null;
    for (let d = r.lo; d <= r.hi; d += r.step) days.add(d);
  }
  if (days.size === 0) return null;
  return Array.from(days).toSorted((a, b) => a - b);
}

/** 把星期集合按最大连续段序列化：[1,2,3,4,5] → "1-5"、[0,1,2,4] → "0-2,4"、
 *  [0,2,4,6] → "0,2,4,6"。到这里任何一对都值得折叠成区间——"0-1" 比 "0,1" 短。 */
function dowFieldFromDays(days: number[]): string {
  return consecutiveRuns(days)
    .map(([lo, hi]) => (hi > lo ? `${lo}-${hi}` : `${lo}`))
    .join(",");
}

/** 把小时字段解析成"间隔 + 可选窗口"。模型的间隔按小时计，所以装不下的步长没有
 *  "每 N 小时"形式——那种退化形态到那一步早已被 collapseDegenerateRange 重写成
 *  固定值了。 */
function parseHourPattern(
  field: string,
): { interval: number; range: { lo: number; hi: number } | null } | null {
  const r = parseRangePart(field, HOUR);
  if (r === null || r.step > HOUR.max) return null;
  // "*" 与 "0-23" 选出同样的小时，而模型把全天只有一种表示——窗口的缺席。编辑器的
  // 窗口如今常驻屏上，两种写法之间没有可让它们差异化的开关状态了：横跨全天的窗口
  // 只会让一个本来就全天的计划上亮着"重置"控件，还给同一计划两套结构化形态。
  const spansDay = r.wildcard || (r.lo === HOUR.min && r.hi === HOUR.max);
  return { interval: r.step, range: spansDay ? null : { lo: r.lo, hi: r.hi } };
}

/** `*` → 1，`*\/N` 与 `0/N` → N。锚定步长（"15/5"）需要一个模型不携带的分偏移，
 *  所以保持仅高级。 */
function parseMinuteInterval(field: string): number | null {
  const r = parseRangePart(field, MINUTE);
  if (r === null || r.explicitRange || r.step > MINUTE.max) return null;
  // 步长必须从整点 0 分出发："*/5" 与 "0/5" 是同一集合，"15/5" 则是模型没有偏移槽
  // 的一种。裸 "0" 是固定分，不是间隔——但 parseTimeFields 在到达这里之前就读掉了。
  if (r.lo !== MINUTE.min) return null;
  if (!r.stepped && !r.wildcard) return null;
  return r.step;
}

// 从分/时两个字段解析出 TimePattern。
function parseTimeFields(minuteField: string, hourField: string): TimePattern | null {
  const plainMinute = plainValue(minuteField, MINUTE);

  // 固定分：要么是固定时刻，要么是每 N 小时模式。
  if (plainMinute !== null) {
    const plainHour = plainValue(hourField, HOUR);
    if (plainHour !== null) {
      return { kind: "at", time: `${pad2(plainHour)}:${pad2(plainMinute)}` };
    }
    const hour = parseHourPattern(hourField);
    if (hour === null) return null;
    return {
      kind: "every",
      unit: "hours",
      interval: hour.interval,
      minute: plainMinute,
      window:
        hour.range === null
          ? null
          : {
              from: `${pad2(hour.range.lo)}:${pad2(plainMinute)}`,
              to: `${pad2(hour.range.hi)}:${pad2(plainMinute)}`,
            },
    };
  }

  // 通配/步长的分：每 N 分钟模式带小时粒度窗口。模型只携带一个步长，所以真实的小
  // 时步长不能与分步长叠加——但步长 1 选出每小时，那只是未步长字段的展开写法
  // （"*/1" ≡ "*"）。
  const interval = parseMinuteInterval(minuteField);
  if (interval === null) return null;
  let window: { from: string; to: string } | null;
  const hour = parseHourPattern(hourField);
  if (hour === null) return null;
  if (hour.range === null) {
    // 全天小时字段——"*/N" 或表达同义的 "0-23/N"——在其步长真实时就是第二个步长
    // 维度；模型只有一个步长。
    if (hour.interval !== 1) return null;
    window = null;
  } else {
    const { lo, hi } = hour.range;
    // 比所跨区间还宽的步长永远走不出该区间的首小时，所以它选的就是那一小时——
    // 一小时窗口，而非第二个步长。"9-9/3" 与 "9-21/23" 在这里都指 09:00–09:59。
    const selectsOneHour = hour.interval > hi - lo;
    if (!selectsOneHour && hour.interval !== 1) return null;
    window = { from: `${pad2(lo)}:00`, to: `${pad2(selectsOneHour ? lo : hi)}:59` };
  }
  return { kind: "every", unit: "minutes", interval, minute: 0, window };
}

/** 后端接受三类下面的解析器不接受的词法变化：它用 strings.FieldsFunc 切列表字段，
 *  会 DROP 空元素（"1-5," 对它而言是周一至周五，",9" 是第 9 小时，"1,,5" 是
 *  {周一, 周五}）；它用 strconv.Atoi 解析每个数字，能吃前导零与正号（"009"、"+9"
 *  与 "00/05" 分别是 9、9 与 0/5）；它还只用区间的 LOW END 单独判定是不是通配——
 *  见 normalizeRange。每个字段在进入下面的任一解析器之前都要在这里归一化一次：
 *  若让解析器自己记这条规则，一旦它忘了，就会把服务器跑得高高兴兴的计划搞到整页
 *  控件置灰。 */
function normalizeField(field: string): string {
  return field
    .split(",")
    .filter((part) => part.length > 0)
    .map(normalizeRange)
    .map((part) =>
      part.replace(/\+?\d+/g, (num) => {
        // 超过安全整数范围的数字会让服务端 Atoi 溢出、是语法错误；留着不改，
        // 下面的解析器会以同样方式拒绝它们。
        const n = Number(num);
        return Number.isSafeInteger(n) ? String(n) : num;
      }),
    )
    .join(",");
}

/** robfig 用区间 LOW END 测 "*" 或 "?"（parser.go:261），命中就把整个区间当作该
 *  字段的全宽——绝不再看 "-" 后面是什么。所以 "?" 就是 "*"，"*-19"、"?-5" 甚至
 *  "*-19-25" 也都是：上端不只是不检查，是不读。步长仍会保留（"?/2" 是 "*\/2"，
 *  "*-19/2" 是 "*\/2"——低端定全宽，步长照样走），而这里只重写低端，所以 "9-?"
 *  保持它对服务器而言的语法错误身份。
 *
 *  怪异但关键：对将执行计划的解析器它们都是通配，编辑器若宣称"无法表达"就会在
 *  一个明明"每小时"的表达式上把控件全置灰。 */
function normalizeRange(part: string): string {
  const slash = part.indexOf("/");
  const range = slash === -1 ? part : part.slice(0, slash);
  const step = slash === -1 ? "" : part.slice(slash);
  const low = range.split("-")[0];
  return low === "*" || low === "?" ? `*${step}` : part;
}

/** 比所跨区间还宽的步长永远走不出该区间首值，所以它表达一个固定值：分钟上的
 *  "*\/65" 是 0、小时上的 "*\/24" 是 0、"10-20/30" 是 10、月日上的 "*\/40" 是 1 号。
 *  服务器全都跑得好好的，而固定值一直是模型有槽可放的东西。把它们重写为那个值，
 *  控件才接得住；不重写，编辑器就会在一个自己完全理解、还要向用户不实地宣称
 *  "控件表达不了"的计划上把自己置灰。
 *
 *  `rangeSlot` 标记一个模型按"区间"而非"值"持有的字段：小时，且只有小时。这样的
 *  字段在解析器仍能精确读它时绝不折叠——无步长的 "9-9"（编辑器在拖拽窗口起点追上
 *  终点时写出的单小时窗口）与带步长的 "9-9/3"（间隔为 3 的同一窗口）都不能退回成
 *  固定时刻，否则用户设的 every/unit/window 控件会在重载时被悄悄丢弃。只有超出
 *  模型 interval 能容纳的步长（> max）才在小时上折叠，因为 parseHourPattern 会拒绝
 *  它、表达式会把编辑器置灰。分钟与月日持有的是值不是区间，所以它们任何形式都折叠，
 *  含 "5-5"。
 *
 *  在月日上安全：那里 star 位决定 dom 与 dow 是 OR 还是 AND——robfig 对任何大于 1
 *  的步长清掉该位（parser.go:297），所以 "*\/40" 已是受限字段，"1" 也是——折叠不会
 *  把 "1 号，或任意周一" 翻转成 "1 号，且是周一"。月份不碰：无论钉在哪个月都已超出
 *  模型。 */
function collapseDegenerateRange(
  field: string,
  bounds: FieldBounds,
  rangeSlot: boolean,
): string {
  return field
    .split(",")
    .map((part) => {
      const r = parseRangePart(part, bounds);
      // 完全不是该语法的形状：放它走，让字段自己的解析器拒绝它、把表达式送进
      // 仅高级。
      if (r === null) return part;
      // 区间槽字段保留其解析器仍能精确读的每个区间：无步长的（"9-9"），以及在
      // 模型 interval 范围内的带步长（"9-9/3"）。
      if (rangeSlot && !r.stepped) return part;
      if (rangeSlot && r.explicitRange && r.step <= bounds.max) return part;
      return r.step > r.hi - r.lo ? String(r.lo) : part;
    })
    .join(",");
}

/** 列表里含步长 1 的通配元素时，选的就是字段的全部值，含 star 位：robfig 把列表
 *  各部分做 OR，通配部分已覆盖整个字段，步长 1 正是保住其 star 位的方式
 *  （parser.go:297）——所以 "1,*" 就是 "*"，一路包含那位在 dom 与 dow 之间驱动
 *  AND-or-OR 的选择。下面的解析器只在模型可枚举的字段（星期）读列表，不做这个
 *  重写的话，"0,*" 的分字段会在"每分钟"的计划上把整页编辑器置灰。
 *
 *  只有每一部分都能解析时才折叠："*,abc" 与 "*,60" 对服务器是语法错误，只读了
 *  通配部分的折叠会把服务器拒绝的表达式结构化。全带真实步长的通配列表
 *  （"*\/2,*\/3"）本身不覆盖任何东西，留给字段自己的解析器判定。 */
function collapseWildcardList(field: string, bounds: FieldBounds): string {
  const parts = field.split(",");
  if (parts.length < 2) return field;
  const ranges = parts.map((part) => parseRangePart(part, bounds));
  if (ranges.some((r) => r === null)) return field;
  return ranges.some((r) => r!.wildcard && r!.step === 1) ? "*" : field;
}

/** robfig 的 Parse 从中读时区的两个前缀。 */
export function hasTimezonePrefix(s: string): boolean {
  return s.startsWith("TZ=") || s.startsWith("CRON_TZ=");
}

/** robfig 的 Parse 在读字段前从表达式开头读一个可选的 `TZ=` / `CRON_TZ=` 前缀——
 *  大小写敏感、作用于未 trim 的文本、到第一个字面空格为止（parser.go 的
 *  `strings.Index(spec, " ")`）——内嵌时区随后覆盖计划自己的时区列：两者走同一个
 *  LoadLocation，所以 `CRON_TZ=Asia/Tokyo 0 9 * * *` 配上 UTC 列会在东京时间 09:00
 *  触发。带前缀的表达式本身就是（时区, 字段）这一对，把它拆进编辑器的时区字段是
 *  精确的——正是它让时区选择器在面对一个会静默凌驾于其上的表达式时仍然诚实。
 *
 *  对 robfig 读不出前缀、以及它读得出但编辑器不能碰的形状返回 null：
 *  - 完全没有空格（"TZ=UTC"）：robfig v3.0.1 会对它 panic（parser.go:99），所以
 *    原样留在高级模式，由服务端（有护栏）判它非法；
 *  - 第一个前缀后面紧跟着第二个前缀：robfig 剥掉一个前缀，把第二个当字段读，拆解
 *    会把一个被拒的表达式悄悄变成被接受的；
 *  - 选择器给不出的时区（isPickerZone）。config.timezone 既是选择器值又是格式化
 *    参数，只有浏览器能放住的名字才被拆出。"TZ=Local 0 9 * * *"——服务端合法，
 *    指 SERVER 宿主时区——原样留在高级模式照旧运行；服务器会拒绝的时区也如此，
 *    其诚实的回显就是在服务端自己的报错下展示原始文本。 */
export function extractTimezonePrefix(expr: string): { timezone: string; rest: string } | null {
  if (!hasTimezonePrefix(expr)) return null;
  const space = expr.indexOf(" ");
  if (space === -1) return null;
  const name = expr.slice(expr.indexOf("=") + 1, space);
  const rest = expr.slice(space).trim();
  if (rest.length === 0) return null;
  if (hasTimezonePrefix(rest)) return null;
  // 空名在服务端按 UTC 加载（LoadLocation("")）；把这话说破。
  const timezone = canonicalPickerZone(name === "" ? "UTC" : name);
  if (timezone === null) return null;
  return { timezone, rest };
}

/** 把时区规范成选择器列表拼写的形式；浏览器根本放不住（作为选择器值或 Intl
 *  格式化参数都放不住）时返回 null。Intl 大小写不敏感地读时区名，所以
 *  "asia/shanghai" 对它也是真时区；把那种拼写放进 config 会在列表自己的
 *  "Asia/Shanghai" 条目旁座出一个重复项。resolvedOptions 会规范化名字——这也正是
 *  服务器 LoadLocation 保证能加载的拼写（小写只在碰巧大小写不敏感的文件系统上
 *  才成功过）。浏览器与服务器真正分道扬镳的地方（"Local"、满是制表符的垃圾），
 *  以浏览器为准，因为 config.timezone 是浏览器侧的值。 */
function canonicalPickerZone(tz: string): string | null {
  try {
    return new Intl.DateTimeFormat(undefined, { timeZone: tz }).resolvedOptions().timeZone;
  } catch {
    return null;
  }
}

/** 把 cron 表达式解析回结构化配置；解析不出的部分一律落进 `raw`（仅高级模式），
 *  结构化模型仍由编辑器默认值占位。
 */
export function parseCron(expr: string, timezone: string): ScheduleConfig {
  // 前缀无论其余部分能否结构化都先拆出：时区无论如何都乘进 `timezone`，`raw`
  // 只留字段。这与 toCron 相反——后者在出去时把前缀装回去——同时它也水合那些
  // 在表达式携带前缀之前存下的旧行，调用方把时区列作为回退传进来。
  const prefix = extractTimezonePrefix(expr);
  const tz = prefix === null ? timezone : prefix.timezone;
  const body = prefix === null ? expr : prefix.rest;
  const advanced: ScheduleConfig = {
    ...getDefaultScheduleConfig(tz),
    raw: body,
  };
  const parts = body.trim().split(/\s+/);
  if (parts.length !== 5) return advanced;
  const [rawMinute, rawHour, rawDom, rawMonth, rawDow] = parts.map(normalizeField) as [
    string,
    string,
    string,
    string,
    string,
  ];
  const minuteField = collapseDegenerateRange(collapseWildcardList(rawMinute, MINUTE), MINUTE, false);
  const hourField = collapseDegenerateRange(collapseWildcardList(rawHour, HOUR), HOUR, true);
  const domField = collapseDegenerateRange(collapseWildcardList(rawDom, DOM), DOM, false);
  const monthField = collapseWildcardList(rawMonth, MONTH);
  const dowField = collapseWildcardList(rawDow, DOW);

  if (monthField !== "*") return advanced;

  let days: DayPattern;
  if (domField === "*") {
    if (dowField === "*") {
      days = { kind: "every" };
    } else {
      const daysOfWeek = expandDow(dowField);
      if (daysOfWeek === null) return advanced;
      days = { kind: "weekly", daysOfWeek };
    }
  } else {
    // 钉死月日只有在 dow 不受限时才能结构化：cron 把 dom+dow 当 OR，那是模型无法
    // 表达的。
    if (dowField !== "*") return advanced;
    const dayOfMonth = plainValue(domField, DOM);
    if (dayOfMonth === null) return advanced;
    days = { kind: "monthly", dayOfMonth };
  }

  const time = parseTimeFields(minuteField, hourField);
  if (time === null) return advanced;

  return { time, days, timezone: tz, raw: null };
}

/** 线上表达式：时区乘在 cron 字符串里，作为 `TZ=` 前缀，每次保存、每次预览都是。
 *  robfig 在读计划自己的时区列之前先读内嵌时区，所以"裸表达式 + 列"是两个真源、
 *  之间有静默覆盖规则；无条件加前缀就只剩一个。列仍会写一份，作为镜像：展示面与
 *  预览端点的 invalid_timezone 归类读它，且它永远与前缀一致。
 *
 *  唯一不前缀的形状是：本身已带自己前缀的高级 `raw`——那是一段等待服务器裁决的
 *  输入表达式（见 applyDraft），再叠一个前缀会让 robfig 把第一个当前缀、第二个当
 *  字段。它一字不改地出去——服务端胜出的是内嵌时区，这正是文本所言的。 */
export function toCron(config: ScheduleConfig): string {
  const fields = cronFields(config);
  if (hasTimezonePrefix(fields)) return fields;
  // 首尾空白经不起加前缀——robfig（以及 parseCron）在时区后 trim 文本，加前缀会
  // 悄悄改掉那些恰好让该表达式成为被拒之身的字符——而空表达式也没有可加前缀之物。
  if (fields === "" || fields !== fields.trim()) return fields;
  // 带空格的时区名会在那个空格处被 robfig 切断、把其余交给字段解析器。IANA 名
  // 从不含空格（选择器给不出这种值），所以这里只护一份损坏的 config——它降级为
  // 时区列已覆盖的裸表达式。
  if (config.timezone === "" || config.timezone.includes(" ")) return fields;
  return `TZ=${config.timezone} ${fields}`;
}

/** 计划的五个字段本身——cron 框的可编辑文本，它的 `TZ=` 前缀是从 config.timezone
 *  拉来的固定组输入段，不是框里的字符。高级表达式在这里一字不改地往返。 */
export function cronFields(config: ScheduleConfig): string {
  if (config.raw !== null) return config.raw;

  let dom = "*";
  let dow = "*";
  if (config.days.kind === "monthly") {
    dom = String(config.days.dayOfMonth);
  } else if (config.days.kind === "weekly") {
    // 编辑器始终保持至少选中一天；这里防御性地序列化成周一，而不是吐一个空字段。
    dow = config.days.daysOfWeek.length > 0 ? dowFieldFromDays(config.days.daysOfWeek) : "1";
  }

  const { time } = config;
  let minuteField: string;
  let hourField: string;
  if (time.kind === "at") {
    const at = timeParts(time.time);
    minuteField = String(at.minute);
    hourField = String(at.hour);
  } else if (time.unit === "hours") {
    if (time.window === null) {
      minuteField = String(time.minute);
      hourField = time.interval === 1 ? "*" : `*/${time.interval}`;
    } else {
      const from = timeParts(time.window.from);
      const to = timeParts(time.window.to);
      minuteField = String(from.minute);
      hourField =
        time.interval === 1
          ? `${from.hour}-${to.hour}`
          : `${from.hour}-${to.hour}/${time.interval}`;
    }
  } else {
    minuteField = time.interval === 1 ? "*" : `*/${time.interval}`;
    if (time.window === null) {
      hourField = "*";
    } else {
      const from = timeParts(time.window.from);
      const to = timeParts(time.window.to);
      hourField = from.hour === to.hour ? String(from.hour) : `${from.hour}-${to.hour}`;
    }
  }
  return `${minuteField} ${hourField} ${dom} * ${dow}`;
}
