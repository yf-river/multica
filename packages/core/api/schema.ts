// 本文件提供 API 响应校验工具：用 zod schema 校验接口返回，失败时降级而不是崩溃。
import type { ZodType } from "zod";
import { type Logger, noopLogger } from "../logger";

// 模块级的 schema 告警日志。默认空实现，避免测试运行时刷屏；
// 平台层在应用启动时通过 setSchemaLogger 注入真实 logger。
let schemaLogger: Logger = noopLogger;

/** 设置 schema 校验告警时使用的 logger（应用启动时由平台层调用一次）。 */
export function setSchemaLogger(logger: Logger): void {
  schemaLogger = logger;
}

/** parseWithFallback / parseOrThrow 的选项。 */
export interface ParseOptions {
  /** 端点标识，写进告警日志，方便在生产遥测里按端点检索是哪个契约漂移了。 */
  endpoint: string;
}

/**
 * 用 zod schema 校验从 API 响应解析出来的 JSON 值，成功返回解析结果，失败返回 fallback。
 *
 * 失败时记录一条带端点名和 zod 结构化错误的告警，但绝不抛异常——UI 层必须能继续渲染。
 * 这就是把「接口契约漂移」从白屏事故降级为「降级但仍能渲染」的边界防线。
 *
 * 返回类型锚定在 T（由 fallback 推断），而不是 schema 的 z.infer 类型。schema 有意设计得
 * **宽松**（字符串枚举保留为 z.string() 让未知枚举值也能通过等），所以解析出的运行时值可能
 * 比调用处的严格 TS 类型更宽。调用方通过把 fallback 声明成期望的 T 来断言兼容性；下游代码
 * 本来就要负责用带 default 分支的 switch 和可选链来处理未知枚举值。
 *
 * 何时用这个，见 CLAUDE.md 的「API Response Compatibility」。
 *
 * @param data 从接口响应解析出的原始 JSON 值
 * @param schema 用于校验的 zod schema
 * @param fallback 校验失败时返回的降级值
 * @param opts 校验选项（必须带 endpoint）
 * @returns 校验通过时的解析值，或校验失败时的 fallback
 */
export function parseWithFallback<T>(
  data: unknown,
  schema: ZodType,
  fallback: T,
  opts: ParseOptions,
): T {
  const result = schema.safeParse(data);
  if (result.success) return result.data as T;
  schemaLogger.warn(
    `API response failed schema validation: ${opts.endpoint}`,
    {
      endpoint: opts.endpoint,
      issues: result.error.issues,
      received: data,
    },
  );
  return fallback;
}

/**
 * 同 parseWithFallback，但校验失败时直接抛错（而不是返回降级值）。
 *
 * 用于那些「宁可失败也不该返回伪造结果」的接口——比如创建类写操作，一个读不出 id 的成功响应
 * 绝不能当作创建成功。失败前同样会记录结构化告警。
 *
 * @param data 从接口响应解析出的原始 JSON 值
 * @param schema 用于校验的 zod schema
 * @param opts 校验选项；mayHaveCommitted 标记该请求可能已提交（用于上层决定是否安全重试）
 * @returns 校验通过的解析值
 */
export function parseOrThrow<T>(data: unknown, schema: ZodType, opts: ParseOptions & { mayHaveCommitted?: boolean }): T {
  const result = schema.safeParse(data);
  if (result.success) return result.data as T;
  schemaLogger.warn(`API response failed schema validation: ${opts.endpoint}`, {
    endpoint: opts.endpoint,
    issues: result.error.issues,
    received: data,
  });
  throw new Error(`API response format invalid: ${opts.endpoint}`);
}
