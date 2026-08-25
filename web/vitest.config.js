import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Pure-logic unit tests only (no DOM/component rendering), so the plain node
// environment is enough. The Svelte plugin is loaded purely for the runes
// transform: .svelte.js state modules (unifiedlog store) use $state/$derived
// and must compile before vitest can import them.
export default defineConfig({
  plugins: [svelte()],
  test: {
    environment: 'node',
    include: ['src/**/*.test.js'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: './coverage',
      include: ['src/lib/**/*.{js,svelte}'],
      exclude: ['src/lib/**/*.test.js'],
    },
  },
})
