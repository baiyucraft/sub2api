import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue(), {
    name: 'sandbox-entry',
    generateBundle() {
      // Classic scripts load in an opaque-origin sandbox without module CORS.
      this.emitFile({ type: 'asset', fileName: 'index.html', source: `<!doctype html>
<html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Codex STATE</title><link rel="stylesheet" href="./style.css"></head><body><div id="app"></div><script src="./app.js"></script></body></html>` })
    },
  }],
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  build: {
    lib: { entry: 'src/main.ts', name: 'CodexStateUI', formats: ['iife'], fileName: () => 'app.js' },
    cssCodeSplit: false,
    rollupOptions: { output: { assetFileNames: '[name].[ext]' } },
  },
})
