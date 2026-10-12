"use client";

import type { ReactNode } from "react";
import { Target, type LucideIcon } from "lucide-react";
import { useT } from "../i18n";
import { PageHeader } from "../layout/page-header";

function LivingPage({
  title,
  description,
  icon: Icon,
  children,
}: {
  title: string;
  description: string;
  icon: LucideIcon;
  children: ReactNode;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader>
        <Icon className="size-4 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-body font-medium">{title}</h1>
      </PageHeader>
      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-5xl space-y-6 px-4 py-8 md:px-8">
          <p className="text-body text-muted-foreground">{description}</p>
          {children}
        </div>
      </main>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="rounded-xl border bg-card p-5 md:p-6">
      <h2 className="mb-4 text-title-sm font-medium">{title}</h2>
      {children}
    </section>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <p className="py-6 text-body text-muted-foreground">{children}</p>;
}


export { MealsPage } from "../meals/meals-page";

export function GoalsPage() {
  const { t } = useT("layout");
  return (
    <LivingPage title={t(($) => $.nav.goals)} description={t(($) => $.living_pages.goals.description)} icon={Target}>
      <Section title={t(($) => $.living_pages.goals.active)}><Empty>{t(($) => $.living_pages.goals.active_empty)}</Empty></Section>
      <div className="grid gap-6 md:grid-cols-2">
        <Section title={t(($) => $.living_pages.goals.completed)}><Empty>{t(($) => $.living_pages.goals.completed_empty)}</Empty></Section>
        <Section title={t(($) => $.living_pages.goals.review)}><Empty>{t(($) => $.living_pages.goals.review_empty)}</Empty></Section>
      </div>
    </LivingPage>
  );
}
