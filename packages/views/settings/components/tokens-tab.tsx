import { GongfengCredentialPanel } from "./gongfeng-credential-panel";
import { TapdCredentialPanel } from "./tapd-credential-panel";

export function TokensTab() {
  return (
    <div className="space-y-4">
      <GongfengCredentialPanel />
      <TapdCredentialPanel />
    </div>
  );
}
