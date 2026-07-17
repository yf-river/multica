#!/usr/bin/env node

import { main } from "./package-runner.mjs";

process.exitCode = main({
  argv: process.argv.slice(2),
  platform: process.platform,
  arch: process.arch,
  env: process.env,
});
