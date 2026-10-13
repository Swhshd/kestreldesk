// This file has been modified for the KestrelDesk project.
import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
    // Generated public bundles are served as static assets, not lintable source.
    "public/flowgram-editor/**",
    "public/sdk/kestreldesk-sdk.min.js",
  ]),
]);

export default eslintConfig;
