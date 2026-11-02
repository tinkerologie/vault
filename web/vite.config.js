import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  experimental: {
    // Only url() in the built CSS is relative, so it resolves when the plugin serves
    // the app from /plugins/vault/ui/, where app.php can rewrite index.html but not
    // the CSS. index.html stays absolute, so the daemon's fallback loads on any path.
    renderBuiltUrl: (_filename, { hostType }) => (hostType === 'css' ? { relative: true } : undefined),
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Keep woff2 as separate files. Inlined as base64 (the default under 4 kB)
    // a retro font would ride along in the main CSS and be downloaded by every
    // theme; as a file it's fetched only when the Retro style selects the family.
    assetsInlineLimit: (filePath) => (filePath.endsWith('.woff2') ? false : undefined),
  },
  server: {
    proxy: {
      '/api': 'http://localhost:24085',
    },
  },
})
