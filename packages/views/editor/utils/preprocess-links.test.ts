import { preprocessLinks } from "@multica/ui/markdown/linkify";
import { describe, expect, it } from "vitest";

describe("preprocessLinks", () => {
  it.each([
    // CJK punctuation terminates a URL without consuming the following prose.
    [
      "见 https://example.com/path。然后继续",
      "见 [https://example.com/path](https://example.com/path)。然后继续",
    ],
    [
      "打开 https://example.com/a，以及其他",
      "打开 [https://example.com/a](https://example.com/a)，以及其他",
    ],
    [
      "两个地址 https://a.com/x、https://b.com/y",
      "两个地址 [https://a.com/x](https://a.com/x)、[https://b.com/y](https://b.com/y)",
    ],
    [
      "（见 https://example.com/x）后文",
      "（见 [https://example.com/x](https://example.com/x)）后文",
    ],
    [
      "「https://example.com/a」后文",
      "「[https://example.com/a](https://example.com/a)」后文",
    ],
    [
      "太好了 https://example.com/x！继续",
      "太好了 [https://example.com/x](https://example.com/x)！继续",
    ],
    [
      "已合并 PR #1623：https://github.com/multica-ai/multica/pull/1623。merge commit",
      "已合并 PR #1623：[https://github.com/multica-ai/multica/pull/1623](https://github.com/multica-ai/multica/pull/1623)。merge commit",
    ],
    [
      "https://github.com/x/y/issues/1619。我接下来把这个",
      "[https://github.com/x/y/issues/1619](https://github.com/x/y/issues/1619)。我接下来把这个",
    ],
    [
      "visit https://example.com/path. next.",
      "visit [https://example.com/path](https://example.com/path). next.",
    ],
    [
      "go https://example.com/path",
      "go [https://example.com/path](https://example.com/path)",
    ],
    [
      "https://zh.wikipedia.org/wiki/中国 参考",
      "[https://zh.wikipedia.org/wiki/中国](https://zh.wikipedia.org/wiki/中国) 参考",
    ],
    ["见 [link](https://example.com/x。)后文", "见 [link](https://example.com/x。)后文"],
    [
      "数据来源：[NBA.com Schedule](https://www.nba.com/schedule)、[NBC Insider](https://www.nbc.com/nbc-insider/every-nba-playoff-game-this-week-on-nbc-peacock-april-25-28)",
      "数据来源：[NBA.com Schedule](https://www.nba.com/schedule)、[NBC Insider](https://www.nbc.com/nbc-insider/every-nba-playoff-game-this-week-on-nbc-peacock-april-25-28)",
    ],
    [
      "数据来源：[NBA.com Schedule](https://www.nba.com/schedule)，官网 NBA.com",
      "数据来源：[NBA.com Schedule](https://www.nba.com/schedule)，官网 [NBA.com](http://NBA.com)",
    ],
    // Bare project filenames stay text; explicit schemes and real domains remain links.
    ["决策已锁定，plan.md 已更新", "决策已锁定，plan.md 已更新"],
    ["see README.md for details", "see README.md for details"],
    ["run build.sh then main.rs and app.py", "run build.sh then main.rs and app.py"],
    ["open https://plan.md now", "open [https://plan.md](https://plan.md) now"],
    ["官网 NBA.com", "官网 [NBA.com](http://NBA.com)"],
    ["plan.md，example.com", "plan.md，[example.com](http://example.com)"],
    ["see ./src/main.go here", "see [./src/main.go](./src/main.go) here"],
    // Email addresses are deliberately not auto-linked.
    ["contact alice@example.com for access", "contact alice@example.com for access"],
    [
      "contact mailto:alice@example.com for access",
      "contact mailto:alice@example.com for access",
    ],
  ])("transforms %j", (input, expected) => {
    expect(preprocessLinks(input)).toBe(expected);
  });
});
