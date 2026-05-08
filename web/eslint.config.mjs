import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default [
  { ignores: [".next/**", "out/**", "node_modules/**"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    plugins: {
      // Register react-hooks so the existing
      // `// eslint-disable-next-line react-hooks/exhaustive-deps`
      // pragmas resolve to a real rule. Pre-fix every disable was
      // a lint error itself ("Definition for rule '…' was not
      // found"), and CI exited 1 on every push.
      "react-hooks": reactHooks,
    },
    rules: {
      // Allow `any` in narrow places — used in api-client for parsed body
      // typing where we don't have a known shape.
      "@typescript-eslint/no-explicit-any": "warn",
      // Treat _-prefixed args/vars as intentionally unused. Standard
      // TS convention; lets us write `(_e) => …` for ignored event
      // handlers without satisfying the linter twice.
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],
      // Inherit the rule levels from react-hooks/recommended without
      // pulling its preset in via spread (the v5 preset is flat-config-
      // shaped and keys differ across releases). Both rules are
      // pure-source-code analysis; cheap to enable.
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
    },
  },
];
