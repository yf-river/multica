// 本文件职责：skills 相关的纯工具函数。

/**
 * 判断某条错误消息是否表示「重名冲突」。
 *
 * 用正则同时匹配 409 状态码 / conflict / already exists / unique constraint
 * 等关键词（不区分大小写），用于把重名错误单独识别出来、给出更友好的提示。
 */
export function isNameConflictError(msg: string): boolean {
  return /\b(409|conflict|already exists|unique constraint)\b/i.test(msg);
}
