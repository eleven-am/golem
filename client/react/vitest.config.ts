import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  resolve: {
    alias: { '@eleven-am/golem-client': fileURLToPath(new URL('../core/src/index.ts', import.meta.url)) },
  },
  test: {
    include: ['test/**/*.test.ts', 'test/**/*.test.tsx'],
    environment: 'node',
    setupFiles: ['test/dom.ts'],
    globalSetup: ['../core/test/harness/global-setup.ts'],
    testTimeout: 30_000,
    hookTimeout: 600_000,
    fileParallelism: false,
    teardownTimeout: 40_000,
  },
})
