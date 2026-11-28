import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import versions from "../../version/version.json" with { type: "json" };

const fontPackages = new URL("./node_modules/@fontsource/", import.meta.url);

export default defineConfig({
  resolve: { alias: { "@fontsource": fileURLToPath(fontPackages).replace(/[\\/]$/, "") } },
  build: { assetsInlineLimit: (path) => path.endsWith(".woff2") ? false : undefined },
  plugins: [{
    name: "sonoryx-web-version",
    transformIndexHtml: () => [{
      tag: "meta",
      attrs: { name: "sonoryx-web-version", content: versions.apps.web },
      injectTo: "head",
    }],
  }, {
    name: "sonoryx-font-licenses",
    generateBundle() {
      const licenses = readdirSync(fontPackages, { withFileTypes: true }).filter((entry) => entry.isDirectory())
        .map(({ name }) => name).sort().map((name) => `@fontsource/${name}\n\n${readFileSync(new URL(`${name}/LICENSE`, fontPackages), "utf8")}`);
      this.emitFile({ type: "asset", fileName: "assets/font-licenses.txt", source: licenses.join("\n\n") });
    },
  }],
});
