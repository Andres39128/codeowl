// eslint.config.mjs — config propio de la imagen (§9.4): los configs del repo
// revisado se ignoran; un eslint.config.js de repo es JS ejecutable en el
// sandbox y además permitiría silenciar la propia detección.
import js from "@eslint/js";
import tsParser from "@typescript-eslint/parser";

export default [
  // Parser de TS para archivos TypeScript (el core no los parsea).
  {
    files: ["**/*.ts", "**/*.tsx", "**/*.mts", "**/*.cts"],
    languageOptions: {
      parser: tsParser,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
  },
  js.configs.recommended,
];
