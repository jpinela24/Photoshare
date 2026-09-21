import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Vite 8 targets a newer baseline than Vite 5 did and will happily emit CSS
// media-query range syntax (`@media (width<=768px)`), which Safari below 16.4
// and older Android WebViews ignore outright — that would silently drop the
// whole responsive layout on older phones. Pin the floor to Vite 5's default
// so the upgrade is a security fix and not a compatibility change.
const BROWSER_TARGETS = ['chrome87', 'edge88', 'firefox78', 'safari14']

export default defineConfig({
  plugins: [react()],
  build: {
    target: BROWSER_TARGETS,
    cssTarget: BROWSER_TARGETS,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080'
    }
  }
})
