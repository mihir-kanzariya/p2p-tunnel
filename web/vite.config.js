import { defineConfig } from 'vite'

export default defineConfig({
  base: process.env.BUILD_TARGET === 'ghpages' ? '/p2p-tunnel/' : '/',
  build: {
    outDir: process.env.BUILD_TARGET === 'ghpages' ? 'dist-ghpages' : 'dist',
  },
})
