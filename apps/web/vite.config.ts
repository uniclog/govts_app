import { defineConfig } from "vite";
import versions from "../../version/version.json" with { type: "json" };

export default defineConfig({
  plugins: [{
    name: "sonoryx-web-version",
    transformIndexHtml: () => [{
      tag: "meta",
      attrs: { name: "sonoryx-web-version", content: versions.apps.web },
      injectTo: "head",
    }],
  }],
});
