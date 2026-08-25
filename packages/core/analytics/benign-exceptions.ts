// Browser resumes ResizeObserver delivery on the next frame, so its loop
// notification is the one current exception safe to drop entirely.
const BENIGN_MESSAGE_PATTERN = /ResizeObserver loop/i;

/**
 * Whether this `$exception` event is known-benign browser noise that should be
 * dropped entirely. Reads the message from the (pre-redaction) event
 * properties — the matched messages carry no PII, so reading them raw is safe,
 * and matching before redaction avoids any chance of a scrub mangling the
 * signal. Never throws: any unexpected shape returns `false` (keep the event),
 * the fail-open direction `before_send` requires.
 */
export function isBenignException(
  properties: Record<string, unknown> | undefined,
): boolean {
  if (!properties || typeof properties !== "object") return false;

  const messages: unknown[] = [properties.$exception_message];
  const list = properties.$exception_list;
  if (Array.isArray(list)) {
    for (const entry of list) {
      if (entry && typeof entry === "object" && "value" in entry) {
        messages.push((entry as { value: unknown }).value);
      }
    }
  }

  for (const message of messages) {
    if (typeof message !== "string") continue;
    if (BENIGN_MESSAGE_PATTERN.test(message)) return true;
  }
  return false;
}
