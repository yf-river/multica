// 本文件提供一套带命名空间、带颜色前缀的浏览器控制台日志工具。

// 日志级别，从小到大表示严重程度递增。
type LogLevel = "debug" | "info" | "warn" | "error";

// 各日志级别在控制台里显示的前缀颜色（CSS 样式字符串）。
const COLORS: Record<LogLevel, string> = {
  debug: "color:#888",
  info: "color:#2196F3",
  warn: "color:#FF9800",
  error: "color:#F44336;font-weight:bold",
};

// 各日志级别对应的 console 方法。
const CONSOLE_METHOD: Record<LogLevel, "log" | "info" | "warn" | "error"> = {
  debug: "log",
  info: "info",
  warn: "warn",
  error: "error",
};

/** 日志器接口，提供 debug/info/warn/error 四个级别的打印方法。 */
export interface Logger {
  debug(msg: string, ...data: unknown[]): void;
  info(msg: string, ...data: unknown[]): void;
  warn(msg: string, ...data: unknown[]): void;
  error(msg: string, ...data: unknown[]): void;
}

/**
 * 创建一个日志器，所有输出都会带上 `[namespace]` 前缀和时间戳、级别颜色，
 * 方便在控制台里区分不同模块、不同级别的日志。
 *
 * @param namespace - 日志器的命名空间（通常是模块名），会显示在每条日志前缀中
 * @returns 一个带命名空间的日志器实例
 */
export function createLogger(namespace: string): Logger {
  const make =
    (level: LogLevel) =>
    (msg: string, ...data: unknown[]) => {
      const ts = new Date().toISOString().slice(11, 23);
      const prefix = `%c${ts} [${namespace}]`;
      if (data.length > 0) {
        console[CONSOLE_METHOD[level]](prefix, COLORS[level], msg, ...data);
      } else {
        console[CONSOLE_METHOD[level]](prefix, COLORS[level], msg);
      }
    };

  return {
    debug: make("debug"),
    info: make("info"),
    warn: make("warn"),
    error: make("error"),
  };
}

/** 空操作的日志器，用于不需要打印日志的场景（例如测试或静默模式）。 */
export const noopLogger: Logger = {
  debug() {},
  info() {},
  warn() {},
  error() {},
};
