"use client";

// 引导流程第三步「和 Mika 开始」的介绍块：用标题 + 一张带占位图标的名片把
// Mika 当作一个对象介绍给用户。
import {
  Item,
  ItemContent,
  ItemDescription,
  ItemMedia,
  ItemTitle,
} from "@multica/ui/components/ui/item";
import { useT } from "../../i18n";

/**
 * Mika 的占位头像 Emoji。它必须和服务端 `server/internal/handler/mika_agent.go`
 * 里的 `mikaAgentAvatarURL` 保持一致：在 Mika 还没有正式形象前，两处共用这个
 * 占位符号；一旦各自改动，引导页看到的脸和真实创建出来的 Agent 就不是同一张了。
 */
export const MIKA_PLACEHOLDER_EMOJI = "🦄";
import { StepHeading } from "./step-shell";

/**
 * 「和 Mika 开始」的正文卡片：把 Mika 当作一个要介绍的对象来呈现，而不是脚注。
 *
 * 这一步以前用技术前提命名（"选一个 Agent 运行时"），Mika 只出现在灰色引导语里
 * ——等于把「要创建的东西」写进小字、把技术前提放在大标题，结果用户可能点了个
 * 写着「和 Mika 开始」的按钮，却根本不知道 Mika 是谁。改成大标题 + 带图标的
 * 名片卡之后，就算用户没仔细读，也能对 Mika 有印象。
 *
 * 走到这一步时 Mika 还不存在——她是在用户提交后才被创建出来的——所以这里没法渲染
 * 她真正保存的头像，只能复用运行时页面「和 Mika 开始」入口用的同一个占位标记，
 * 让两个入口在视觉上认得出是同一件事。
 */
export function MikaIntro() {
  const { t } = useT("onboarding");
  return (
    <div className="flex flex-col gap-5">
      <StepHeading title={t(($) => $.mika_intro.headline)} />
      <Item variant="outline">
        <ItemMedia>
          <span
            role="img"
            aria-label={t(($) => $.mika_intro.name)}
            className="flex size-9 shrink-0 select-none items-center justify-center rounded-full bg-muted text-title leading-none"
          >
            {MIKA_PLACEHOLDER_EMOJI}
          </span>
        </ItemMedia>
        <ItemContent>
          <ItemTitle>
            {t(($) => $.mika_intro.name)}
            <span className="text-muted-foreground">
              {" · "}
              {t(($) => $.mika_intro.role)}
            </span>
          </ItemTitle>
          <ItemDescription>{t(($) => $.mika_intro.blurb)}</ItemDescription>
        </ItemContent>
      </Item>
    </div>
  );
}
