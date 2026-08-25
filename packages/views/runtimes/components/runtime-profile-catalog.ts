import {
  RUNTIME_PROFILE_PROTOCOL_FAMILIES,
  type RuntimeProfile,
  type RuntimeProtocolFamily,
} from "@multica/core/types";

// A single row in the runtimes catalog the management dialog renders: the
// built-in protocol families ship as read-only reference rows, while custom
// profiles are the user's editable assets.
export type RuntimeCatalogEntry =
  | {
      kind: "builtin";
      // Stable row id — the protocol family doubles as the key for built-ins.
      id: string;
      protocolFamily: RuntimeProtocolFamily;
    }
  | {
      kind: "custom";
      id: string;
      protocolFamily: RuntimeProtocolFamily;
      profile: RuntimeProfile;
    };

export interface RuntimeCatalogSections {
  customs: RuntimeCatalogEntry[];
  builtins: RuntimeCatalogEntry[];
}

// Re-export the whitelist as a typed array so callers (the family picker,
// the catalog builder) share the single source of truth.
export const PROTOCOL_FAMILIES: readonly RuntimeProtocolFamily[] =
  RUNTIME_PROFILE_PROTOCOL_FAMILIES;

// buildRuntimeCatalog keeps user-owned custom profiles separate from built-in
// protocol families. The dialog renders customs as the primary management
// surface and built-ins as a collapsed reference section.
export function buildRuntimeCatalog(
  profiles: RuntimeProfile[],
): RuntimeCatalogSections {
  const builtins: RuntimeCatalogEntry[] = PROTOCOL_FAMILIES.map((family) => ({
    kind: "builtin" as const,
    id: `builtin:${family}`,
    protocolFamily: family,
  }));

  const customs: RuntimeCatalogEntry[] = [...profiles]
    .sort((a, b) => {
      if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
      const aTime = Date.parse(a.updated_at) || 0;
      const bTime = Date.parse(b.updated_at) || 0;
      if (aTime !== bTime) return bTime - aTime;
      return a.display_name.localeCompare(b.display_name, undefined, {
        sensitivity: "base",
      });
    })
    .map((profile) => ({
      kind: "custom" as const,
      id: profile.id,
      protocolFamily: profile.protocol_family,
      profile,
    }));

  return { customs, builtins };
}

export interface ProfileFormValues {
  displayName: string;
  commandName: string;
  description: string;
  fixedArgs: string;
}

export type ProfileFormErrorField = "displayName" | "commandName";

// Pure, synchronous validation for the create/edit form. Returns the set of
// invalid fields (empty = valid). Display name and command name are the only
// hard-required fields; description and fixed args are optional.
export function validateProfileForm(
  values: ProfileFormValues,
): ProfileFormErrorField[] {
  const errors: ProfileFormErrorField[] = [];
  if (!values.displayName.trim()) errors.push("displayName");
  if (!values.commandName.trim()) errors.push("commandName");
  return errors;
}

export function parseFixedArgsInput(value: string): string[] {
  return value
    .split("\n")
    .map((arg) => arg.trim())
    .filter(Boolean);
}

export function formatFixedArgsInput(args: string[]): string {
  return args.join("\n");
}

// Returns true when the entry should be treated as a built-in (read-only).
