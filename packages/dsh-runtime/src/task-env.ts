/** Return the one credential DSH child tools may receive from Multica. */
export function multicaTerminalEnvironment(
  environment: NodeJS.ProcessEnv,
): Record<string, string> {
  const token = environment.MULTICA_TOKEN
  return token?.startsWith('mat_') === true && token.length > 4
    ? { MULTICA_TOKEN: token }
    : {}
}
