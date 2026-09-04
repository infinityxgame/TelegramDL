import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import fs from 'fs'
import path from 'path'
import os from 'os'

let backendPort = process.env.TGDL_PORT || '8000'
let backendHost = process.env.TGDL_BIND_HOST || '127.0.0.1'
if (backendHost === '0.0.0.0') {
  backendHost = '127.0.0.1'
}

// Leer dinámicamente ~/.tgdown/.env
try {
  const userEnvPath = path.join(os.homedir(), '.tgdown', '.env')
  if (fs.existsSync(userEnvPath)) {
    const lines = fs.readFileSync(userEnvPath, 'utf8').split('\n')
    for (const rawLine of lines) {
      const line = rawLine.trim()
      if (line.startsWith('TGDL_PORT=')) {
        const val = line.split('=')[1]?.trim()
        if (val) backendPort = val
      } else if (line.startsWith('TGDL_BIND_HOST=')) {
        const val = line.split('=')[1]?.trim()
        if (val && val !== '0.0.0.0') backendHost = val
      }
    }
  }
} catch (e) {
  // Ignorar errores si no existe
}

const proxyTarget = `http://${backendHost}:${backendPort}`

export default defineConfig({
  base: '/',
  plugins: [vue()],
  server: {
    host: '0.0.0.0',
    port: 8080,
    allowedHosts: true,
    clearScreen: false,
    proxy: {
      '/api': {
        target: proxyTarget,
        ws: true,
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  }
})
