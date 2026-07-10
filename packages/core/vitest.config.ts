import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    include: ["**/*.test.{ts,tsx}"],
    // Turbo runs package suites concurrently, so each suite must not claim
    // every host CPU and exhaust the shared worker pool.
    maxWorkers: 4,
    passWithNoTests: true,
  },
});
