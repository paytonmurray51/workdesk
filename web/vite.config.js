import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';

export default defineConfig({
  plugins: [preact()],
  build: { outDir: 'dist', emptyOutDir: true, assetsDir: 'assets' },
  // `npm run dev` proxies the API to a locally running Go server.
  server: { proxy: { '/api': 'http://localhost:8080', '/auth': 'http://localhost:8080' } },
});
