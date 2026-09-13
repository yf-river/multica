// 本文件提供用于「百分比灰度分桶」的确定性 FNV-1a 32 位哈希。
//
// 同一对 (key, identifier) 必须永远算出同一个桶；否则用户会在不同请求之间
// 反复进出实验组。算法与 Go 侧 server/pkg/featureflag/hash.go 逐字节一致，
// 保证同一个开关在前端求值和在后端求值对同一个用户落在同一桶。跨语言的
// 一致性由两侧的金样测试（golden test）守护：见 hash.test.ts 和 hash_test.go。
//
// 哈希作用于每个输入的 UTF-8 编码。Go 的 `[]byte(s)` 转换也是 UTF-8，所以
// 即使开关名或标识符含非 ASCII 字符（中文开关名、带重音符号的用户 ID、
// emoji……），两端实现也保持一致。若直接用 `charCodeAt` 会去哈希 UTF-16
// 码元，遇到任何非 ASCII 输入都会和 Go 悄悄分叉。
//
// 选 FNV-1a 是因为它廉价、零依赖、对小于 100 桶的灰度足够均匀。它不是
// 加密哈希，除了分桶之外不要拿它干别的。

// 每个模块共享一个 TextEncoder。TextEncoder 属于 WHATWG Encoding 规范，
// 所有常青浏览器、Node 11+ 以及 React Native（Hermes）>= 0.74 都内置。
// 这里刻意不做惰性初始化，让失败在 import 时就暴露，而不是等到第一次读开关。
const utf8 = new TextEncoder();

/**
 * 计算若干字符串拼接后的 FNV-1a 32 位哈希。
 *
 * 与 Go 侧实现保持一致：各部分之间插入零字节分隔符（最后一段之后不加）。
 *
 * @param parts - 参与哈希的字符串片段
 * @returns 无符号 32 位哈希值
 */
function fnv1a(parts: ReadonlyArray<string>): number {
  // 32 位 FNV-1a：初始偏移 0x811c9dc5，素数 0x01000193。
  let hash = 0x811c9dc5;
  for (let p = 0; p < parts.length; p++) {
    if (p > 0) {
      // 段与段之间插入零字节分隔符（末段之后不加）。这与 Go 侧在 key 和
      // identifier 之间写 h.Write([]byte{0}) 一致，正是它阻止了 ("ab","c")
      // 和 ("a","bc") 这类组合撞进同一个桶。若多加一个尾部分隔符就会与 Go
      // 分叉，悄悄破坏跨层桶的一致性。
      hash ^= 0;
      hash = Math.imul(hash, 0x01000193);
    }
    // 先把片段按 UTF-8 编码，对齐 Go 的 `[]byte(string)`。直接用 charCodeAt
    // 会去哈希 UTF-16 码元，任何非 ASCII 输入（中文 key、带重音的用户 ID、
    // emoji……）都会和 Go 分叉。详见文件顶部说明。
    const bytes = utf8.encode(parts[p]!);
    for (let i = 0; i < bytes.length; i++) {
      hash ^= bytes[i]!;
      // 乘 FNV 素数并对 2^32 取模。Math.imul 让结果保持在 32 位整数内，
      // 不会滑进浮点数域。
      hash = Math.imul(hash, 0x01000193);
    }
  }
  // 取模前先强制转无符号 32 位，对齐 Go 的 uint32 运算。
  return hash >>> 0;
}

/**
 * 返回 (key, identifier) 在 [0, 100) 中的确定性桶号。
 * 与 server/pkg/featureflag/hash.go 里的 bucketFor 完全一致。
 *
 * @param key - 开关名
 * @param identifier - 分桶标识（默认场景下是 user_id 或 workspace_id）
 * @returns 0–99 的桶号
 */
export function bucketFor(key: string, identifier: string): number {
  return fnv1a([key, identifier]) % 100;
}

/**
 * 判断 (key, identifier) 是否落在前 `percent` 个桶内。
 * 超出 [0, 100] 的比例会被夹回：<=0 表示对所有人关闭，>=100 表示对所有人打开。
 *
 * @param key - 开关名
 * @param identifier - 分桶标识
 * @param percent - 灰度比例（0–100）
 * @returns 是否命中灰度区间
 */
export function inPercent(key: string, identifier: string, percent: number): boolean {
  if (percent <= 0) return false;
  if (percent >= 100) return true;
  return bucketFor(key, identifier) < percent;
}
