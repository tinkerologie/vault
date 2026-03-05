import js from "@eslint/js";
import svelte from "eslint-plugin-svelte";
import globals from "globals";

export default [
  js.configs.recommended,
  ...svelte.configs.recommended,
  {
    languageOptions: {
      globals: {
        ...globals.browser,
        requestAnimationFrame: 'readonly',
        cancelAnimationFrame: 'readonly',
        clearInterval: 'readonly',
        ResizeObserver: 'readonly',
      },
    },
    rules: {
      'no-unused-vars': ['error', {
        argsIgnorePattern: '^_',
        varsIgnorePattern: '^_',
        caughtErrorsIgnorePattern: '^_',
      }],
      'svelte/no-unnecessary-state-wrap': 'off',
    },
  },
  {
    ignores: ["dist/"],
  },
];
