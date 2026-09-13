// 本文件提供「集成频道」的小图标组件：按频道名渲染对应的平台品牌小图标，
// 用在「设置 → 集成」页面各分区的标题行，让读者一眼认出这节属于哪个平台。

import { DingTalkMark } from "./dingtalk-mark";
import { LarkMark } from "./lark-mark";
import { SlackMark } from "./slack-mark";
import { TelegramMark } from "./telegram-mark";
import { WecomMark } from "./wecom-mark";

/** 当前支持的第三方集成频道名。 */
type IntegrationChannel = "lark" | "slack" | "dingtalk" | "wecom" | "telegram";

// 每个频道都用自己的品牌图形，绝不用 lucide 的通用图形代替：图标是读者判断
// 「这一节属于哪个平台」的关键线索，换成通用的气泡或插头图形等于什么都没说
// （参见 WecomMark 的说明，问题记录在 #6585）。lucide-react 本身不带品牌图标，
// 所以接入新频道前，必须先在 *-mark.tsx 里备好它自己的品牌图标，才能列在这里。
/**
 * 集成频道小图标：把频道名映射到对应的品牌图标并统一尺寸（钉钉因图形视觉
 * 偏小用 h-5，其余用 h-4），包一层 span 用于对齐。
 * @param props.channel - 频道名，取值见 IntegrationChannel。
 */
export function IntegrationChannelIcon({ channel }: { channel: IntegrationChannel }) {
  const icon = {
    lark: <LarkMark className="h-4 w-4" />,
    slack: <SlackMark className="h-4 w-4" />,
    dingtalk: <DingTalkMark className="h-5 w-5" />,
    wecom: <WecomMark className="h-4 w-4" />,
    telegram: <TelegramMark className="h-4 w-4" />,
  }[channel];

  return (
    <span
      aria-hidden="true"
      data-testid={`integration-channel-icon-${channel}`}
      className="flex size-5 shrink-0 items-center justify-center text-muted-foreground"
    >
      {icon}
    </span>
  );
}
