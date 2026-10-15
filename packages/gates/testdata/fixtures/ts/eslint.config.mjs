import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["jest/**", "vitest/**"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    rules: {
      complexity: ["error", 5],
      eqeqeq: "warn",
    },
  },
);
