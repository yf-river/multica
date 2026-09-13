"use client";

// 云端版「等待列表」报名表单：填入邮箱（+可选的用途说明）即可报名加入云端等待列表，
// 展开展现在引导流程选择「本地/云端」运行时那一步里。提交成功后表单会锁定，
// 是否离开当前页面由调用方决定。
import { useState } from "react";
import { ArrowRight, Check, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { joinCloudWaitlist } from "@multica/core/onboarding";
import { useT } from "../../i18n";

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const REASON_MAX = 500;

/**
 * 云端等待列表的内联报名表单——目前用在引导流程第 3 步（`StepPlatformFork`
 * 的 cloud 分支）里。
 *
 * 提交时调用 `joinCloudWaitlist` 并把表单置为禁用。它不会自己推进引导流程——
 * 后续导航由调用方负责（通常是底部的「暂时跳过」）。这样职责划分清晰：
 * 等待列表只负责收集意向，真正退出引导走的是 Skip。
 *
 * @param submitted 是否已提交成功（提交后表单锁定为只读）
 * @param onSubmitted 提交成功后的回调
 */
export function CloudWaitlistExpand({
  submitted,
  onSubmitted,
}: {
  submitted: boolean;
  onSubmitted: () => void;
}) {
  const { t } = useT("onboarding");
  const [email, setEmail] = useState("");
  const [reason, setReason] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const canSubmit =
    !submitted &&
    !submitting &&
    EMAIL_PATTERN.test(email.trim()) &&
    reason.trim().length <= REASON_MAX;

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      await joinCloudWaitlist(email.trim(), reason.trim());
      toast.success(t(($) => $.cloud_waitlist.success_toast));
      onSubmitted();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t(($) => $.cloud_waitlist.failed_toast),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex flex-col gap-4 rounded-lg border bg-muted/40 p-5">
      <p className="text-label leading-[1.55] text-foreground">
        {t(($) => $.cloud_waitlist.intro_main)}{" "}
        <span className="text-muted-foreground">
          {t(($) => $.cloud_waitlist.intro_warning)}
        </span>
      </p>

      <div className="flex flex-col gap-1.5">
        <Label
          htmlFor="waitlist-email"
          className="text-caption font-medium text-muted-foreground"
        >
          {t(($) => $.cloud_waitlist.email_label)}
        </Label>
        <Input
          id="waitlist-email"
          type="email"
          autoComplete="email"
          value={email}
          disabled={submitted}
          placeholder={t(($) => $.cloud_waitlist.email_placeholder)}
          onChange={(e) => setEmail(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
        />
      </div>

      <div className="flex flex-col gap-1.5">
        <Label
          htmlFor="waitlist-reason"
          className="text-caption font-medium text-muted-foreground"
        >
          {t(($) => $.cloud_waitlist.reason_label)}
          <span className="ml-2 font-normal text-muted-foreground">
            {t(($) => $.cloud_waitlist.optional)}
          </span>
        </Label>
        <Textarea
          id="waitlist-reason"
          value={reason}
          disabled={submitted}
          onChange={(e) => setReason(e.target.value)}
          placeholder={t(($) => $.cloud_waitlist.reason_placeholder)}
          rows={3}
          maxLength={REASON_MAX}
        />
      </div>

      <div className="flex items-center justify-end">
        <Button size="lg" disabled={submitted || !canSubmit} onClick={submit}>
          {submitting && <Loader2 className="h-4 w-4 animate-spin" />}
          {submitted ? (
            <>
              <Check className="h-4 w-4" />
              {t(($) => $.cloud_waitlist.on_list)}
            </>
          ) : (
            <>
              {t(($) => $.cloud_waitlist.join)}
              <ArrowRight className="h-4 w-4" />
            </>
          )}
        </Button>
      </div>
    </div>
  );
}
