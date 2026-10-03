import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss()
  ],

  // 开发服务器配置 (Wails v3 dev 模式)
  server: {
    port: 5173,
    strictPort: true,
    host: '0.0.0.0',
  },

  // 构建配置
  build: {
    // 输出到 dist 目录 (Wails 会嵌入此目录)
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,

    // Rollup 配置
    rollupOptions: {
      output: {
        // 手动分包
        manualChunks: {
          'react-vendor': ['react', 'react-dom'],
          'charts': ['recharts'],
          'icons': ['lucide-react']
        }
      }
    },

    // 优化配置
    minify: 'terser',
    terserOptions: {
      compress: {
        drop_console: true,
        drop_debugger: true
      }
    },
    target: 'es2015',
    chunkSizeWarningLimit: 1000
  },

  // 路径别名
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '@components': path.resolve(__dirname, './src/components'),
      '@pages': path.resolve(__dirname, './src/pages'),
      '@hooks': path.resolve(__dirname, './src/hooks'),
      '@contexts': path.resolve(__dirname, './src/contexts'),
      '@utils': path.resolve(__dirname, './src/utils'),
      '@wailsjs': path.resolve(__dirname, './src/bindings')
    }
  },

  // CSS 配置
  css: {
    devSourcemap: true
  }
})
