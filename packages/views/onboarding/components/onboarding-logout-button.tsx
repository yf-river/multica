"use client";

// 引导流程里每个步骤都有的「退出 / 切换账号」逃生出口按钮。
import { LogOut } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useLogout } from "../../auth";
import { useT } from "../../i18n";

/**
 * 引导流程各步骤共用的退出登录按钮（同时用于切换账号）。
 *
 * 只有欢迎页用 `fixed` 固定在角落，因为它没有侧边栏可依附；其余步骤都传
 * `inline`，让按钮顺着进度栏的底部排列。之前把它钉在窗口角落会超出版心的
 * 衡量范围、跑到「上一步」按钮上面，看起来像第二行标题。
 *
 * inline 的意思是「放在侧边进度栏上」。进度栏把 `.dark` 作用域限定在自己的
 * 面板里，所以普通的 muted/foreground 颜色变量在深色背景上已能正确显示，
 * 无需再手动叠加反色工具类。早期版本在这里手拼 `text-background/60`，
 * 结果在黑底上显示成近乎不可见的灰。
 *
 * @param inline 是否按正常文档流渲染（放在进度栏底部），而不是固定在角落
 */
export function OnboardingLogoutButton({
  inline = false,
}: {
  /** 是否按正常文档流渲染（放在进度栏底部），而不是固定在角落。 */
  inline?: boolean;
} = {}) {
  const { t } = useT("onboarding");
  const logout = useLogout();

  return (
    <Button
      variant="ghost"
      size="sm"
      className={cn(
        inline
          ? "-ml-2 w-fit shrink-0 text-muted-foreground hover:text-foreground"
          : "fixed right-8 top-8 z-50 text-muted-foreground hover:text-destructive",
      )}
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      onClick={logout}
    >
      <LogOut />
      {t(($) => $.common.log_out)}
    </Button>
  );
}
