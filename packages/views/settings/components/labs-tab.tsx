// 本文件是「实验（Labs）」设置标签页。曾经放在这里的开关陆续有了专属归属
// （见下），如今它只充当未来实验性开关的占位容器，保持导航层级稳定、不被删除。

"use client";

import { FlaskConical } from "lucide-react";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@multica/ui/components/ui/empty";
import { useT } from "../../i18n";
import { SettingsCard, SettingsTab } from "./settings-layout";

// 原本在 Labs 里的「提交附带 Co-authored-by（共同作者）署名」开关已挪进 GitHub
// 专属设置页（见 github-tab.tsx）。这里保留 Labs 标签，作为未来实验性功能开关的
// 容器而非从导航里删掉——避免改动信息架构。
/**
 * 「实验」标签页。当前没有任何实验开关，只渲染一个空态占位提示。
 */
export function LabsTab() {
  const { t } = useT("settings");
  return (
    <SettingsTab title={t(($) => $.page.tabs.labs)}>
      <SettingsCard>
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <FlaskConical className="h-4 w-4" />
            </EmptyMedia>
            <EmptyTitle>{t(($) => $.labs.section_placeholder_title)}</EmptyTitle>
            <EmptyDescription>
              {t(($) => $.labs.section_placeholder_description)}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </SettingsCard>
    </SettingsTab>
  );
}
