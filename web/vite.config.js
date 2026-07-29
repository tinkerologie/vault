import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Keep woff2 as separate files. Inlined as base64 (the default under 4 kB)
    // a retro font would ride along in the main CSS and be downloaded by every
    // theme; as a file it's fetched only when a retro style selects the family.
    assetsInlineLimit: (filePath) => (filePath.endsWith('.woff2') ? false : undefined),
  },
  server: {
    proxy: {
      '/api': 'http://localhost:24085',
    },
  },
})
