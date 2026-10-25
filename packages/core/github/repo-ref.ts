/**
 * Checkout-ref helpers for `github_repo` project resources.
 *
 * The ref pins where a project's tasks START — `multica repo checkout` falls
 * back to the remote default branch when it is empty, and an explicit
 * `--ref` on the command line still wins over it. It is not a promise that
 * work lands on that branch, and it does not retarget pull requests.
 *
 * Lives in core (not views) because the mobile app needs the same rules and
 * cannot import from the web-only package.
 */

/**
 * Git's own limit is the filesystem's, but a loose ref path has to fit in a
 * file name. 255 is the conservative ceiling every platform honors, and it is
 * far past any real branch name — the cap exists to stop a paste of an entire
 * document from reaching the database, not to police naming.
 */
export const GIT_REF_MAX_LENGTH = 255;

export type GitRefInvalidReason =
  | "too_long"
  | "invalid_characters"
  | "invalid_format";

export type GitRefValidation =
  | { ok: true }
  | { ok: false; reason: GitRefInvalidReason };

/**
 * Reject input git itself could never resolve, and nothing more.
 *
 * Mirrors the subset of `git check-ref-format` that applies to a ref a user
 * types: it must accept every branch, tag and commit SHA we advertise, so the
 * rules here are about SHAPE only. Whether the ref exists on the remote is a
 * different question answered at checkout time — the daemon may be offline and
 * the repository may be private, so existence must never gate saving config.
 *
 * An empty ref is valid: it means "use the repository's default branch".
 */
export function validateGitRef(ref: string): GitRefValidation {
  const value = ref.trim();
  if (value === "") return { ok: true };
  if (new TextEncoder().encode(value).length > GIT_REF_MAX_LENGTH) return { ok: false, reason: "too_long" };

  // ASCII control characters, DEL, space, and the characters git reserves for
  // its own revision syntax. Scanned by code point rather than matched with a
  // regex so the control range stays readable (and lintable) as a comparison.
  const RESERVED = "~^:?*[\\";
  for (const char of value) {
    const code = char.codePointAt(0)!;
    if (code <= 0x20 || code === 0x7f || RESERVED.includes(char)) {
      return { ok: false, reason: "invalid_characters" };
    }
  }

  // Path-shape rules. `@{` is reflog syntax; a lone `@` is shorthand for HEAD;
  // `..` would read as a range; `.lock` is what git names its lock files.
  if (
    value.includes("..") ||
    value.includes("@{") ||
    value === "@" ||
    value.startsWith("/") ||
    value.startsWith("-") ||
    value.endsWith("/") ||
    value.includes("//") ||
    value.startsWith(".") ||
    value.endsWith(".") ||
    value.endsWith(".lock") ||
    value.split("/").some((segment) => segment.startsWith(".") || segment.endsWith(".lock"))
  ) {
    return { ok: false, reason: "invalid_format" };
  }

  return { ok: true };
}

