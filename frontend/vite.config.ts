import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";

// The interface is built into ../web/ui, which the Go binary embeds with the
// rest of web/. Fixed names: index.html loads ui/ui.js and ui/ui.css.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  base: "./",
  // Monaco's worker, next to ui.js with a fixed name.
  worker: {
    format: "es",
    rollupOptions: { output: { entryFileNames: "ui-[name].js" } },
  },
  build: {
    outDir: "../web/ui",
    emptyOutDir: true,
    target: "es2022",
    sourcemap: false,
    // one file for the whole interface (it is local: no network to save)
    chunkSizeWarningLimit: 1000,
    rollupOptions: {
      input: fileURLToPath(new URL("./src/main.tsx", import.meta.url)),
      output: {
        entryFileNames: "ui.js",
        chunkFileNames: "ui-[name].js",
        assetFileNames: "ui[extname]",
      },
    },
  },
});
