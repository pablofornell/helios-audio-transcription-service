import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    // Forwards /api/v1/* and /readyz to a locally running helios-ats API so
    // a page served from here is same-origin with it — no CORS
    // configuration needed on the Go service (see
    // src/lib/browser/api.ts's module doc).
    //
    // Harmless under `wails dev`/`npm run dev`: the Wails-shell adapter
    // (src/lib/wails/api.ts) never issues a fetch through these paths at
    // all, so this proxy configuration simply goes unused there — it only
    // matters for `npm run dev:web` (see package.json), a plain browser
    // tab with no Wails runtime to fall back from.
    proxy: {
      '/api': {
        target: 'http://localhost:7462',
        changeOrigin: true,
      },
      '/readyz': {
        target: 'http://localhost:7462',
        changeOrigin: true,
      },
    },
  },
})
