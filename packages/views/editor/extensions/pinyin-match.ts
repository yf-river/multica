import { pinyin } from "pinyin-pro";

/**
 * 判断查询串能否按拼音匹配某个名字（供 mention/斜杠命令的中文名过滤用）。
 * 支持：
 * - 全拼前缀匹配："liyunlong" 能命中 "李云龙"
 * - 首字母缩写："lyl" 能命中 "李云龙"
 * - 部分前缀匹配："liyu" 能命中 "李云龙"
 * - 混合匹配（部分字取全拼、部分取首字母）见 {@link hybridMatch}
 */
export function matchesPinyin(name: string, query: string): boolean {
  if (!query) return true;

  // 名字不含汉字就不做拼音匹配。
  if (!/[\u4e00-\u9fff]/.test(name)) return false;

  const q = query.toLowerCase();

  // 取全拼（无声调、无分隔符，ü 转 v 以兼容常规输入）。
  const full = pinyin(name, { toneType: "none", type: "array", v: true });
  const fullStr = full.join("");

  // 全拼前缀匹配："liyunlong" 或 "liyun"。
  if (fullStr.startsWith(q)) return true;

  // 首字母匹配："lyl"。
  const initials = full.map((p) => p[0] || "").join("");
  if (initials.startsWith(q)) return true;

  // 混合匹配：有的字按全拼、有的字按首字母，如 "liyl" 命中 "李云龙"
  // （li + y + l）。
  return hybridMatch(full, q);
}

/**
 * 混合匹配：查询串可以对某些字取全拼、另一些字取首字母，从左到右消费。
 * 例：对 ["li", "yun", "long"]，"liyunl" 能命中（li + yun + l）。
 */
function hybridMatch(pinyinArr: string[], query: string): boolean {
  return match(pinyinArr, 0, query, 0);
}

/** 递归匹配：用 arr[ai:] 的字去拼 q[qi:] 的前缀。 */
function match(arr: string[], ai: number, q: string, qi: number): boolean {
  if (qi >= q.length) return true;
  if (ai >= arr.length) return false;

  const syllable = arr[ai]!;

  // 尝试用当前音节的全拼或其任意前缀去匹配。
  for (let len = syllable.length; len >= 1; len--) {
    if (qi + len > q.length) continue;
    if (q.substring(qi, qi + len) === syllable.substring(0, len)) {
      if (match(arr, ai + 1, q, qi + len)) return true;
    }
  }

  return false;
}
