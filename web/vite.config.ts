import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// viteConfig 将前端资源构建为可嵌入桌面程序的相对路径
export default defineConfig({
  base: './',
  plugins: [react(), tailwindcss()],
  build: {
    target: 'es2023',
    sourcemap: false,
  },
})
