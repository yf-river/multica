"use client";

// CLI 安装指引卡片：展示「装 CLI + 连接云端」两条命令，各带一个一键复制按钮。
import { useState } from "react";
import { Check, Copy, Terminal } from "lucide-react";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { CODE_LIGATURE_CLASS } from "@multica/ui/lib/code-style";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";

const INSTALL_CMD =
  "curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash";
const SETUP_CMD = "multica setup";

function CopyButton({ text }: { text: string }) {
  const { t } = useT("onboarding");
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    void copyText(text).then((ok) => {
      if (!ok) return;
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <button
      type="button"
      onClick={handleCopy}
      className="shrink-0 rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      aria-label={t(($) => $.cli_install.copy_aria)}
    >
      {copied ? (
        <Check className="h-3.5 w-3.5 text-success" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
    </button>
  );
}

function Step({ n, label, cmd }: { n: number; label: string; cmd: string }) {
  return (
    <div>
      <p className="mb-1.5 text-caption font-medium text-foreground">
        {n}. {label}
      </p>
      <div className="flex items-start gap-2 rounded-lg bg-muted px-3 py-2.5 font-mono text-body">
        <Terminal className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <code
          className={cn(
            "min-w-0 flex-1 whitespace-pre-wrap break-all",
            CODE_LIGATURE_CLASS,
          )}
        >
          {cmd}
        </code>
        <CopyButton text={cmd} />
      </div>
    </div>
  );
}

/**
 * CLI 安装指引——就两条「复制并运行」的命令。命令是写死的，因为没有任何环境变量
 * 需要推断：第 1 步是公开的安装脚本，第 2 步是云端 `multica setup`，端点 CLI 自己
 * 知道。本地开发要测自托管变体时，直接在终端里手敲扩展后的命令即可，无需把环境
 * 变量一层层传进 React。
 */
export function CliInstallInstructions() {
  const { t } = useT("onboarding");
  return (
    <Card className="w-full">
      <CardContent className="space-y-4 pt-4">
        <p className="text-caption leading-[1.55] text-muted-foreground">
          {t(($) => $.cli_install.intro)}
        </p>
        <Step n={1} label={t(($) => $.cli_install.step1_label)} cmd={INSTALL_CMD} />
        <Step n={2} label={t(($) => $.cli_install.step2_label)} cmd={SETUP_CMD} />
      </CardContent>
    </Card>
  );
}
