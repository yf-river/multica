import { ArrowLeft } from "lucide-react";
import { DragStrip } from "@multica/views/platform";
import { useT } from "../../i18n";
import { StepHeader } from "./step-header";

export function RuntimeStepHeader({ onBack }: { onBack?: () => void }) {
  const { t } = useT("onboarding");
  return (
    <>
      <DragStrip />

      <header className="flex shrink-0 items-center gap-4 bg-background px-6 py-3 sm:px-10 md:px-14 lg:px-16">
        {onBack ? (
          <button
            type="button"
            onClick={onBack}
            className="flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            {t(($) => $.common.back)}
          </button>
        ) : (
          <span aria-hidden className="w-0" />
        )}
        <div className="flex-1">
          <StepHeader currentStep="runtime" />
        </div>
      </header>
    </>
  );
}
