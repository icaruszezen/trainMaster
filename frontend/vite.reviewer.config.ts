import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

/**
 * 本地审图程序（backend/cmd/trainmaster-reviewer）的界面：入口 reviewer.html，
 * 构建到程序的 web/dist 中，编译程序时嵌入（npm run build:reviewer，或在仓库
 * 根目录 make reviewer）。程序对所有页面路径返回 reviewer.html，资源在 /assets/ 下。
 *
 * 构建时可用 TM_REVIEWER_OUT_DIR 改变输出目录（如 Docker 构建）。
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "./src"),
    },
  },
  build: {
    outDir:
      process.env.TM_REVIEWER_OUT_DIR ||
      resolve(
        import.meta.dirname,
        "../backend/cmd/trainmaster-reviewer/web/dist"
      ),
    emptyOutDir: true,
    // 页面从本机的程序读取，不必为浏览器缓存拆分代码块。
    chunkSizeWarningLimit: 2048,
    rolldownOptions: {
      input: resolve(import.meta.dirname, "reviewer.html"),
    },
  },
})
