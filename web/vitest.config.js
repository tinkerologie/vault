import { configDefaults, defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import { svelteTesting } from '@testing-library/svelte/vite'

// Two test layers share one `vitest run` (#509):
//
//   unit       Pure logic in the node environment: src/lib modules and rune
//              stores. The Svelte plugin is loaded at the root purely for the
//              runes transform: .svelte.js state modules (unifiedlog store)
//              use $state/$derived and must compile before vitest can import
//              them.
//   component  *.component.test.js under src/pages and src/components, rendered
//              in jsdom with @testing-library/svelte. svelteTesting() is added
//              to this project only, so Svelte's browser build (where onMount
//              runs) and auto-cleanup never leak into the node suite.
//
// Coverage stays at the root so CI keeps producing one lcov report.
export default defineConfig({
  plugins: [svelte()],
  test: {
    projects: [
      {
        extends: true,
        test: {
          name: 'unit',
          environment: 'node',
          include: ['src/**/*.test.js'],
          exclude: [...configDefaults.exclude, 'src/**/*.component.test.js'],
        },
      },
      {
        extends: true,
        plugins: [svelteTesting()],
        test: {
          name: 'component',
          environment: 'jsdom',
          include: ['src/pages/**/*.component.test.js', 'src/components/**/*.component.test.js'],
          setupFiles: ['./src/test-support/component-setup.js'],
        },
      },
    ],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: './coverage',
      include: ['src/lib/**/*.{js,svelte}', 'src/pages/Logs.svelte'],
      exclude: ['src/lib/**/*.test.js'],
    },
  },
})
