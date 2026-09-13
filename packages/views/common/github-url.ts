// 对 GitHub URL 返回 "owner/repo"，这样当多个 URL 共享长前缀时，截断后的
// 展示标签仍能保持可辨识。支持：
//   https://github.com/owner/repo[/...]
//   https://www.github.com/owner/repo
//   ssh://git@github.com/owner/repo.git
//   git@github.com:owner/repo.git   （scp 简写）
// .git 后缀在所有情况下都会被去掉。当结果标签仍然很长时，用中间截断保留有区分
// 度的尾部（针对很多仓库共享长公共前缀的情况，如 customer-alpha vs customer-beta）。
// 非 GitHub 输入（企业自建 host、格式错误的字符串）原样返回。
/**
 * 把 GitHub 仓库 URL 压缩成 "owner/repo" 形式的短标签。
 * @param url 原始仓库 URL。
 * @returns 短标签，非 GitHub 输入原样返回。
 */
export function githubShortLabel(url: string): string {
  // scp 简写——new URL() 对这类会抛异常，所以要在 try 块之前匹配。
  const scp = url.match(/^(?:[^@/]+@)?github\.com:([^/]+)\/([^/]+?)(?:\.git)?$/);
  if (scp) return midTruncate(`${scp[1]}/${scp[2]}`);
  try {
    const u = new URL(url);
    if (u.hostname === "github.com" || u.hostname === "www.github.com") {
      const [owner, repo] = u.pathname.split("/").filter(Boolean);
      if (owner && repo) return midTruncate(`${owner}/${repo.replace(/\.git$/, "")}`);
    }
  } catch {
    // 不是可解析的 URL——落到下面原样返回
  }
  return url;
}

// 把字符串中间截断到最多 maxLen 个字符，同时保留头部和尾部。尾部正是区分
// 那些共享长公共前缀的仓库的关键（如 customer-alpha vs customer-beta）。
/**
 * 中间截断字符串到指定最大长度，保留头部与尾部。
 * @param s 原始字符串。
 * @param maxLen 最大长度，默认 40。
 * @returns 截断后的字符串。
 */
export function midTruncate(s: string, maxLen = 40): string {
  if (s.length <= maxLen) return s;
  const tail = Math.floor((maxLen - 1) / 2);
  const head = maxLen - 1 - tail;
  return `${s.slice(0, head)}…${s.slice(-tail)}`;
}
