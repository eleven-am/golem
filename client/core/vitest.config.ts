import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    include: ['test/**/*.test.ts'],
    globalSetup: ['test/harness/global-setup.ts'],
    testTimeout: 30_000,
    hookTimeout: 600_000,
    fileParallelism: false,
    teardownTimeout: 40_000,
  },
})
