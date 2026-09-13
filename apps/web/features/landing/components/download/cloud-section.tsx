"use client";

/**
 * 下载页「云运行时」区块（客户端组件）：复用共享的 CloudWaitlistExpand 申请表单，
 * 配上更适合下载页的标题与副标题。提交动作在子组件内部完成（写入 waitlist），
 * 这里的 submitted 只用来防止本页生命周期内重复提交。
 */
import { useState } from "react";
import { CloudWaitlistExpand } from "@multica/views/onboarding";
import { useLocale } from "../../i18n";

/**
 * 云运行时 waitlist 区块的薄封装：标题 + 共享申请表单。
 *
 * @returns 云运行时申请区块。
 */
export function CloudSection() {
  const { t } = useLocale();
  const d = t.download.cloud;
  const [submitted, setSubmitted] = useState(false);

  return (
    <section className="bg-white py-20 text-[#0a0d12] sm:py-24">
      <div className="mx-auto max-w-[720px] px-4 sm:px-6 lg:px-8">
        <h2 className="landing-serif text-[2.2rem] leading-[1.1] tracking-[-0.03em] sm:text-[2.6rem]">
          {d.title}
        </h2>
        <p className="mt-4 max-w-[560px] text-body-lg leading-7 text-[#0a0d12]/72">
          {d.sub}
        </p>

        <div className="mt-10">
          <CloudWaitlistExpand
            submitted={submitted}
            onSubmitted={() => setSubmitted(true)}
          />
        </div>
      </div>
    </section>
  );
}
