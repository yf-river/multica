import { GongfengCredentialPanel } from "./gongfeng-credential-panel";
import { TapdCredentialPanel } from "./tapd-credential-panel";
import { useT } from "../../i18n";

export function TokensTab() {
  const { t } = useT("settings");
  return (
    <div className="space-y-6">
      <section className="space-y-1">
        <h2 className="text-sm font-semibold">
          {t(($) => $.tokens.external_credentials_title)}
        </h2>
        <p className="max-w-3xl text-sm text-muted-foreground">
          {t(($) => $.tokens.external_credentials_description)}
        </p>
      </section>
      <GongfengCredentialPanel />
      <TapdCredentialPanel />
    </div>
  );
}
