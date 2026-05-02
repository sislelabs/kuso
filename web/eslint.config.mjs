import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default [
  { ignores: [".next/**", "out/**", "node_modules/**"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    rules: {
      // Allow `any` in narrow places — used in api-client for parsed body
      // typing where we don't have a known shape.
      "@typescript-eslint/no-explicit-any": "warn",
    },
  },
];
