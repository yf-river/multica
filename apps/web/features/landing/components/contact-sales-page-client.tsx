"use client";

/**
 * 官网「联系销售」表单页（客户端组件）：纯前端负责渲染表单与交互状态，
 * 收集潜在客户信息后 POST 到 `/api/contact-sales` 转交销售线索。
 * 文案全部来自 landing i18n 字典；拦截常见免费邮箱 / 限流错误并即时提示。
 */
import Link from "next/link";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { LandingHeader } from "./landing-header";
import { LandingFooter } from "./landing-footer";
import { useLocale } from "../i18n";

/** 表单提交状态机：初始 / 提交中 / 成功 / 失败（带提示语）。 */
type SubmitState =
  | { status: "idle" }
  | { status: "submitting" }
  | { status: "success" }
  | { status: "error"; message: string };

/** 表单各字段的值结构。 */
type FormState = {
  firstName: string;
  lastName: string;
  businessEmail: string;
  companyName: string;
  companySize: string;
  countryRegion: string;
  useCase: string;
  goals: string;
  consentOutreach: boolean;
  consentUpdates: boolean;
};

/** 表单初始空值。 */
const EMPTY_FORM: FormState = {
  firstName: "",
  lastName: "",
  businessEmail: "",
  companyName: "",
  companySize: "",
  countryRegion: "",
  useCase: "",
  goals: "",
  consentOutreach: false,
  consentUpdates: false,
};

// 常见免费邮箱域名清单：仅用于客户端即时反馈（提示「请使用企业邮箱」），
// 服务端还有一份更全的独立校验——这里只是体验上的预检，不是安全边界。
const CLIENT_FREE_EMAIL_DOMAINS = new Set([
  "gmail.com",
  "googlemail.com",
  "outlook.com",
  "hotmail.com",
  "live.com",
  "msn.com",
  "yahoo.com",
  "icloud.com",
  "me.com",
  "aol.com",
  "qq.com",
  "163.com",
  "126.com",
  "foxmail.com",
]);

/**
 * 联系销售页内容主体（导出给 page.tsx 使用）。
 *
 * 管理表单状态、提交流程与三种结果展示（成功卡片 / 错误提示 / 表单本体）。
 *
 * @returns 带官网导航与页脚的整页布局。
 */
export function ContactSalesPageClient() {
  const { t } = useLocale();
  const c = t.contactSales;

  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [state, setState] = useState<SubmitState>({ status: "idle" });
  const successAnchorRef = useRef<HTMLDivElement | null>(null);

  const isBusy = state.status === "submitting";

  // 提交成功后，较高的表单会被替换成矮很多的成功卡片；浏览器此时仍停留在原滚动位置，
  // 用户可能正好看到页脚。这里主动把页面滚回顶部锚点，确保感谢信息出现在视野里。
  useEffect(() => {
    if (state.status !== "success") return;
    successAnchorRef.current?.scrollIntoView({
      behavior: "smooth",
      block: "start",
    });
  }, [state.status]);

  const emailDomain = useMemo(() => {
    const at = form.businessEmail.lastIndexOf("@");
    if (at < 0) return "";
    return form.businessEmail.slice(at + 1).trim().toLowerCase();
  }, [form.businessEmail]);

  const emailLooksFree = emailDomain !== "" && CLIENT_FREE_EMAIL_DOMAINS.has(emailDomain);

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((prev) => ({ ...prev, [key]: value }));
  }

  async function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (isBusy) return;
    if (emailLooksFree) {
      setState({ status: "error", message: c.errors.freeEmail });
      return;
    }
    setState({ status: "submitting" });

    try {
      const res = await fetch("/api/contact-sales", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          first_name: form.firstName,
          last_name: form.lastName,
          business_email: form.businessEmail,
          company_name: form.companyName,
          company_size: form.companySize,
          country_region: form.countryRegion,
          use_case: form.useCase,
          goals: form.goals,
          consent_outreach: form.consentOutreach,
          consent_updates: form.consentUpdates,
        }),
      });

      if (res.ok) {
        setState({ status: "success" });
        setForm(EMPTY_FORM);
        return;
      }

      // 429：IP 级中间件或单邮箱提交上限触发的限流。
      if (res.status === 429) {
        setState({ status: "error", message: c.errors.rateLimit });
        return;
      }

      // 尝试解析服务端错误信息；解析不出就用兜底文案。
      let serverMsg = "";
      try {
        const body = (await res.json()) as { error?: unknown };
        if (typeof body?.error === "string") serverMsg = body.error;
      } catch {
        // 忽略——响应体不是 JSON。
      }
      if (/business email/i.test(serverMsg)) {
        setState({ status: "error", message: c.errors.freeEmail });
      } else if (/email/i.test(serverMsg)) {
        setState({ status: "error", message: c.errors.invalidEmail });
      } else {
        setState({ status: "error", message: c.errors.generic });
      }
    } catch {
      setState({ status: "error", message: c.errors.generic });
    }
  }

  return (
    <>
      <LandingHeader variant="light" />
      <main className="bg-[#f7f8fa] text-[#0a0d12]">
        <div
          ref={successAnchorRef}
          className="mx-auto max-w-[760px] px-4 py-12 sm:px-6 sm:py-16 lg:py-20"
        >
          <div className="mb-8 text-center">
            <p className="text-caption font-semibold uppercase tracking-[0.12em] text-[#0a0d12]/45">
              {c.eyebrow}
            </p>
            <h1 className="mt-2 landing-serif text-[2.4rem] leading-[1.1] tracking-[-0.02em] sm:text-[2.8rem]">
              {c.title}
            </h1>
            <p className="mt-3 text-body text-[#0a0d12]/60 sm:text-body-lg">
              {c.subtitle}
            </p>
          </div>

          {state.status === "success" ? (
            <SuccessCard
              title={c.success.title}
              message={c.success.message}
              cta={c.success.cta}
            />
          ) : (
            <FormCard
              form={form}
              update={update}
              emailLooksFree={emailLooksFree}
              busy={isBusy}
              error={state.status === "error" ? state.message : null}
              onSubmit={handleSubmit}
              dict={c}
            />
          )}
        </div>
      </main>
      <LandingFooter />
    </>
  );
}

/** 提交成功后的确认卡片：标题、说明与返回首页按钮。 */
function SuccessCard({
  title,
  message,
  cta,
}: {
  title: string;
  message: string;
  cta: string;
}) {
  return (
    <div className="rounded-[16px] border border-[#0a0d12]/8 bg-white p-8 shadow-[0_1px_2px_rgba(10,13,18,0.04)] sm:p-10">
      <h2 className="landing-serif text-[1.8rem] leading-[1.15] tracking-[-0.02em]">
        {title}
      </h2>
      <p className="mt-3 text-body-lg leading-[1.7] text-[#0a0d12]/70">
        {message}
      </p>
      <div className="mt-6">
        <Link
          href="/"
          className="inline-flex items-center gap-2 rounded-[11px] bg-[#0a0d12] px-5 py-2.5 text-label font-semibold text-white transition-colors hover:bg-[#0a0d12]/88"
        >
          {cta}
        </Link>
      </div>
    </div>
  );
}

/** 表单字段字典的类型（取自 contactSales 文案段）。 */
type FormDict = ReturnType<typeof useLocale>["t"]["contactSales"];

/** 整张联系表单：姓名/邮箱/公司/规模/地区/用途/目标 + 两个营销同意勾选。 */
function FormCard({
  form,
  update,
  emailLooksFree,
  busy,
  error,
  onSubmit,
  dict,
}: {
  form: FormState;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
  emailLooksFree: boolean;
  busy: boolean;
  error: string | null;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void;
  dict: FormDict;
}) {
  const firstNameId = useId();
  const lastNameId = useId();
  const emailId = useId();
  const emailHintId = useId();
  const companyId = useId();
  const sizeId = useId();
  const countryId = useId();
  const useCaseId = useId();
  const goalsId = useId();
  const goalsHintId = useId();
  const consentOutreachId = useId();
  const consentUpdatesId = useId();

  return (
    <form
      onSubmit={onSubmit}
      className="space-y-8 rounded-[16px] border border-[#0a0d12]/8 bg-white p-6 shadow-[0_1px_2px_rgba(10,13,18,0.04)] sm:p-10"
    >
      <div className="rounded-[12px] border border-[#0a0d12]/8 bg-[#f7f8fa] p-4 text-label leading-[1.6] text-[#0a0d12]/72">
        <p className="font-semibold text-[#0a0d12]">
          <span aria-hidden className="mr-1.5">📌</span>
          {dict.notice.badge}
        </p>
        <p className="mt-1">{dict.notice.body}</p>
      </div>

      <div className="grid gap-5 sm:grid-cols-2">
        <Field label={dict.fields.firstName} htmlFor={firstNameId} required>
          <TextInput
            id={firstNameId}
            value={form.firstName}
            onChange={(v) => update("firstName", v)}
            autoComplete="given-name"
            maxLength={80}
            required
            disabled={busy}
          />
        </Field>
        <Field label={dict.fields.lastName} htmlFor={lastNameId} required>
          <TextInput
            id={lastNameId}
            value={form.lastName}
            onChange={(v) => update("lastName", v)}
            autoComplete="family-name"
            maxLength={80}
            required
            disabled={busy}
          />
        </Field>
      </div>

      <div className="grid gap-5 sm:grid-cols-2">
        <Field
          label={dict.fields.businessEmail}
          htmlFor={emailId}
          required
          hint={dict.fields.businessEmailHint}
          hintId={emailHintId}
          tone={emailLooksFree ? "warning" : undefined}
        >
          <TextInput
            id={emailId}
            type="email"
            value={form.businessEmail}
            onChange={(v) => update("businessEmail", v)}
            autoComplete="email"
            maxLength={254}
            required
            disabled={busy}
            ariaDescribedBy={emailHintId}
            ariaInvalid={emailLooksFree || undefined}
          />
        </Field>
        <Field label={dict.fields.companyName} htmlFor={companyId} required>
          <TextInput
            id={companyId}
            value={form.companyName}
            onChange={(v) => update("companyName", v)}
            autoComplete="organization"
            maxLength={200}
            required
            disabled={busy}
          />
        </Field>
      </div>

      <Field label={dict.fields.companySize} htmlFor={sizeId} required>
        <SelectInput
          id={sizeId}
          value={form.companySize}
          onChange={(v) => update("companySize", v)}
          placeholder={dict.fields.selectPlaceholder}
          required
          disabled={busy}
          options={dict.companySizes}
        />
      </Field>

      <Field label={dict.fields.countryRegion} htmlFor={countryId} required>
        <SelectInput
          id={countryId}
          value={form.countryRegion}
          onChange={(v) => update("countryRegion", v)}
          placeholder={dict.fields.selectPlaceholder}
          required
          disabled={busy}
          options={dict.countries.map((c) => ({ value: c, label: c }))}
        />
      </Field>

      <Field label={dict.fields.useCase} htmlFor={useCaseId} required>
        <SelectInput
          id={useCaseId}
          value={form.useCase}
          onChange={(v) => update("useCase", v)}
          placeholder={dict.fields.selectPlaceholder}
          required
          disabled={busy}
          options={dict.useCases}
        />
      </Field>

      <Field
        label={dict.fields.goals}
        htmlFor={goalsId}
        hint={dict.fields.goalsHint}
        hintId={goalsHintId}
      >
        <textarea
          id={goalsId}
          value={form.goals}
          onChange={(e) => update("goals", e.target.value)}
          rows={4}
          maxLength={2000}
          disabled={busy}
          aria-describedby={goalsHintId}
          className="block w-full rounded-[10px] border border-[#0a0d12]/14 bg-white px-3.5 py-2.5 text-body text-[#0a0d12] placeholder:text-[#0a0d12]/35 transition-colors focus:border-[#0a0d12]/40 focus:outline-none focus:ring-2 focus:ring-[#0a0d12]/10 disabled:opacity-60"
        />
      </Field>

      <ConsentBlock
        outreachId={consentOutreachId}
        updatesId={consentUpdatesId}
        form={form}
        update={update}
        dict={dict}
        busy={busy}
      />

      {error ? (
        <p
          role="alert"
          className="rounded-[10px] border border-[#c83a3a]/24 bg-[#fdecec] px-4 py-3 text-label leading-[1.6] text-[#7a1d1d]"
        >
          {error}
        </p>
      ) : null}

      <button
        type="submit"
        disabled={busy}
        className="inline-flex w-full items-center justify-center rounded-[12px] bg-[#f04a2f] px-5 py-3 text-body font-semibold text-white transition-colors hover:bg-[#d63d24] disabled:cursor-not-allowed disabled:opacity-60 sm:w-auto"
      >
        {busy ? dict.fields.submitting : dict.fields.submit}
      </button>
    </form>
  );
}

/** 单个字段的外壳：label + 输入控件 +（可选）提示文字，警告语气会标红。 */
function Field({
  label,
  htmlFor,
  required,
  hint,
  hintId,
  tone,
  children,
}: {
  label: string;
  htmlFor: string;
  required?: boolean;
  hint?: string;
  hintId?: string;
  tone?: "warning";
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <label
        htmlFor={htmlFor}
        className="block text-label font-medium text-[#0a0d12]/82"
      >
        {label}
        {required ? <span className="ml-0.5 text-[#f04a2f]">*</span> : null}
      </label>
      {children}
      {hint ? (
        <p
          id={hintId}
          className={
            tone === "warning"
              ? "text-caption leading-[1.55] text-[#7a1d1d]"
              : "text-caption leading-[1.55] text-[#0a0d12]/55"
          }
        >
          {hint}
        </p>
      ) : null}
    </div>
  );
}

/** 统一样式的文本/邮箱输入框。 */
function TextInput({
  id,
  value,
  onChange,
  type = "text",
  autoComplete,
  maxLength,
  required,
  disabled,
  ariaDescribedBy,
  ariaInvalid,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  autoComplete?: string;
  maxLength?: number;
  required?: boolean;
  disabled?: boolean;
  ariaDescribedBy?: string;
  ariaInvalid?: boolean;
}) {
  return (
    <input
      id={id}
      type={type}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      autoComplete={autoComplete}
      maxLength={maxLength}
      required={required}
      disabled={disabled}
      aria-describedby={ariaDescribedBy}
      aria-invalid={ariaInvalid}
      className="block w-full rounded-[10px] border border-[#0a0d12]/14 bg-white px-3.5 py-2.5 text-body text-[#0a0d12] placeholder:text-[#0a0d12]/35 transition-colors focus:border-[#0a0d12]/40 focus:outline-none focus:ring-2 focus:ring-[#0a0d12]/10 disabled:opacity-60 aria-[invalid=true]:border-[#c83a3a]/60 aria-[invalid=true]:focus:ring-[#c83a3a]/15"
    />
  );
}

/** 统一样式的下拉选择框（带自定义下拉箭头）。 */
function SelectInput({
  id,
  value,
  onChange,
  options,
  placeholder,
  required,
  disabled,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  placeholder: string;
  required?: boolean;
  disabled?: boolean;
}) {
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      required={required}
      disabled={disabled}
      className="block w-full appearance-none rounded-[10px] border border-[#0a0d12]/14 bg-white bg-[url('data:image/svg+xml,%3Csvg%20xmlns%3D%22http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%22%20width%3D%2212%22%20height%3D%228%22%20viewBox%3D%220%200%2012%208%22%20fill%3D%22none%22%3E%3Cpath%20d%3D%22M1%201.5L6%206.5L11%201.5%22%20stroke%3D%22%230a0d12%22%20stroke-opacity%3D%220.5%22%20stroke-width%3D%221.5%22%20stroke-linecap%3D%22round%22%20stroke-linejoin%3D%22round%22%2F%3E%3C%2Fsvg%3E')] bg-[length:12px_8px] bg-[right_14px_center] bg-no-repeat px-3.5 py-2.5 pr-10 text-body text-[#0a0d12] transition-colors focus:border-[#0a0d12]/40 focus:outline-none focus:ring-2 focus:ring-[#0a0d12]/10 disabled:opacity-60"
    >
      <option value="" disabled>
        {placeholder}
      </option>
      {options.map((opt) => (
        <option key={opt.value} value={opt.value}>
          {opt.label}
        </option>
      ))}
    </select>
  );
}

/** 营销同意区块：介绍文字 + 两个勾选项 + 隐私链接。 */
function ConsentBlock({
  outreachId,
  updatesId,
  form,
  update,
  dict,
  busy,
}: {
  outreachId: string;
  updatesId: string;
  form: FormState;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
  dict: FormDict;
  busy: boolean;
}) {
  return (
    <div className="space-y-3 text-label leading-[1.65] text-[#0a0d12]/70">
      <p className="font-semibold text-[#0a0d12]">
        {dict.consent.intro}
      </p>
      <ConsentCheckbox
        id={outreachId}
        checked={form.consentOutreach}
        onChange={(v) => update("consentOutreach", v)}
        disabled={busy}
        label={dict.consent.outreach}
      />
      <ConsentCheckbox
        id={updatesId}
        checked={form.consentUpdates}
        onChange={(v) => update("consentUpdates", v)}
        disabled={busy}
        label={dict.consent.updates}
      />
      <p>
        {dict.consent.unsubscribe}{" "}
        <Link
          href={dict.consent.privacyLinkHref}
          className="text-[#0a0d12] underline decoration-[#0a0d12]/30 underline-offset-2 hover:decoration-[#0a0d12]/60"
        >
          {dict.consent.privacyLinkLabel}
        </Link>
      </p>
      <p>{dict.consent.submitConsent}</p>
    </div>
  );
}

/** 单个同意勾选项。 */
function ConsentCheckbox({
  id,
  checked,
  onChange,
  disabled,
  label,
}: {
  id: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <label htmlFor={id} className="flex items-start gap-2.5">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        disabled={disabled}
        className="mt-0.5 size-4 shrink-0 rounded-[4px] border-[#0a0d12]/30 text-[#0a0d12] focus:ring-[#0a0d12]/20 disabled:opacity-60"
      />
      <span>{label}</span>
    </label>
  );
}
