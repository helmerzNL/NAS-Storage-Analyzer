import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The app is served by the Go binary on the same origin, so API calls use
// relative URLs; `npm run dev` proxies them to a locally running server.
export default defineConfig({
  plugins: [svelte()],
  build: { outDir: '../internal/webui/dist', emptyOutDir: true, sourcemap: false, target: 'es2022' },
  server: { proxy: { '/api': 'http://127.0.0.1:8080', '/openapi.yaml': 'http://127.0.0.1:8080' } },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
});
