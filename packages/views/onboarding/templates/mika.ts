export interface MikaOnboardingDefinition {
  title: string;
}

/**
 * Mika's name, description, avatar, permissions, and system instructions are
 * NOT here — they are server constants delivered by `POST /api/agents/mika`.
 * Keeping them out of the client is what lets Multica update Mika's prompt by
 * deploying, and stops a client from minting an agent that claims Mika's
 * identity.
 *
 * The chat title stays client-side: it names a session this member is opening.
 */
const MIKA_CHAT_TITLE = "和 Mika 开始";

export function getMikaOnboarding(): MikaOnboardingDefinition {
  return {
    title: MIKA_CHAT_TITLE,
  };
}
