import {defineConfig} from 'vitest/config'
export default defineConfig({resolve:{alias:{'@deepseek-ai/schemastery':new URL('./node_modules/@deepseek-ai/schemastery/lib/index.mjs',import.meta.url).pathname}},test:{environment:'node',include:['tests/**/*.spec.ts']}})
