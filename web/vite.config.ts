import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发时把 /api 与 Hermes 代理前缀转发到后端（go run ./cmd/hermes-mock，默认 8080）；
// 生产时前端由 Go //go:embed 提供，无需 proxy。
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    port: 3000,
    proxy: {
      '/api': 'http://localhost:8080',
      // 坐席软电话同源代理（jssip WS + 工作台 SDK 经 Go 反代到 call-center/hermes-ws）
      '/agent-workbench': { target: 'http://localhost:8080', ws: true },
      '/call-center': 'http://localhost:8080',
    },
  },
})
