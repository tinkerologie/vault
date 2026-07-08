import { defineConfig } from 'vitest/config'

// Pure-logic unit tests only (no DOM/component rendering), so the plain node
// environment is enough and we deliberately don't load the Svelte plugin.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.js'],
  },
})
