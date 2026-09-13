import { clsx, type ClassValue } from "clsx"
import { extendTailwindMerge } from "tailwind-merge"

// 本文件是 className 合并工具：clsx 拼类 + tailwind-merge 去冲突。
// 关键在下方为字号档位扩展了 tailwind-merge 的分组，避免把「字号」误判成
//「文字颜色」而互相覆盖。

/**
 * 我们的字号刻度是按角色命名的（`text-body`、`text-caption`），tailwind-merge
 * 区分不了它们和颜色工具类：`text-<x>` 本身有歧义，它内置的表只列了 Tailwind
 * 默认字号。不配置的话，它把所有角色档位都归进 text 颜色组，然后会把先出现的
 * `text-body` / `text-muted-foreground` 其中一个当冲突丢掉——而且是静默地丢，
 * 元素最终以继承的 16px 渲染或颜色不对，源码里却看不出任何原因。
 *
 * 把这些档位注册成 font-size 后，冲突组就正确了：字号跟字号争、颜色跟颜色争，
 * 二者可以共存。此列表必须与 `packages/ui/styles/tokens.css` 里的 `--text-*`
 * 档位保持一致。
 */
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        {
          text: [
            "micro",
            "caption",
            "label",
            "body",
            "body-lg",
            "title-sm",
            "title",
            "title-lg",
            "display-sm",
            "display",
          ],
        },
      ],
    },
  },
})

/** 合并多个 className：先拼接再去掉互相冲突的 Tailwind 工具类 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
