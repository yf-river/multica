"use client";

import { useT } from "../../i18n";
import { ProjectGongfengRepositories } from "./project-gongfeng-repositories";

export function RepositoriesTab() {
  const { t } = useT("settings");

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">{t(($) => $.repositories.section_title)}</h2>
      <ProjectGongfengRepositories />
    </section>
  );
}
