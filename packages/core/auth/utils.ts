/**
 * Validate a post-login redirect URL and return it only if safe to follow.
 *
 * Only single-slash relative paths (e.g. `/issues/abc`) are accepted. Returns
 * `null` for unsafe or empty input — call sites decide the fallback so this
 * helper never overloads a specific path with "user did not pass next".
 *
 * Rejects:
 *   - `null` / empty string
 *   - absolute URLs (`https://evil.com`, `javascript:alert(1)`, …)
 *   - protocol-relative URLs (`//evil.com`)
 *   - paths containing backslashes (Windows-style or `/\\host`)
 *   - paths containing ASCII control characters (`\x00`–`\x1f`)
 */
export function sanitizeNextUrl(raw: string | null): string | null {
  if (!raw) return null;
  if (!raw.startsWith("/") || raw.startsWith("//")) return null;
  // eslint-disable-next-line no-control-regex -- intentional: rejecting control chars is the whole point
  if (/[\x00-\x1f\\]/.test(raw)) return null;
  return raw;
}

const PASSWORD_SPECIAL_CHARS = "!\"#$%&'()*+,-./:;<=>?@[]^_`{|}~";

export interface PasswordValidation {
  valid: boolean;
  message: string;
}

/**
 * Validate password strength:
 * - 8-32 characters
 * - at least 3 of 4 character types (uppercase, lowercase, digit, special)
 * - special characters are restricted to: !"#$%&'()*+,-./:;<=>?@[]^_`{|}~
 */
export function validatePassword(password: string): PasswordValidation {
  if (password.length < 8) {
    return { valid: false, message: "Password must be at least 8 characters" };
  }
  if (password.length > 32) {
    return { valid: false, message: "Password must be at most 32 characters" };
  }

  let hasUpper = false;
  let hasLower = false;
  let hasDigit = false;
  let hasSpecial = false;

  for (const ch of password) {
    if (ch >= "A" && ch <= "Z") {
      hasUpper = true;
    } else if (ch >= "a" && ch <= "z") {
      hasLower = true;
    } else if (ch >= "0" && ch <= "9") {
      hasDigit = true;
    } else if (PASSWORD_SPECIAL_CHARS.includes(ch)) {
      hasSpecial = true;
    } else {
      return { valid: false, message: "Password contains invalid characters" };
    }
  }

  let types = 0;
  if (hasUpper) types++;
  if (hasLower) types++;
  if (hasDigit) types++;
  if (hasSpecial) types++;

  if (types < 3) {
    return {
      valid: false,
      message:
        "Password must contain at least 3 of: uppercase, lowercase, digit, special character",
    };
  }

  return { valid: true, message: "" };
}
