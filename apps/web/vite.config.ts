import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, loadEnv } from 'vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_')
  const live = env.VITE_API_MODE === 'live'
  const target = env.VITE_API_PROXY_TARGET || 'http://localhost:8080'
  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
    },
    server: {
      port: 5173,
      // Live mode: /v1 (REST, SSE, OAuth redirects) goes to services/api. Mock mode: MSW
      // intercepts in the browser, so no proxy. The Origin header is kept (no changeOrigin) so
      // the API's CSRF check sees http://localhost:5173.
      proxy: live ? { '/v1': { target, changeOrigin: false } } : undefined,
    },
    // Main chunk is ~510 kB (React + TanStack + Radix); charts and mocks are split out.
    build: { chunkSizeWarningLimit: 600 },
  }
})
