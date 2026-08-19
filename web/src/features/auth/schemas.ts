// Hand-rolled validator for the login form.
//
// This was a zod schema. zod pulled ~64 KB gzipped into the INITIAL JS
// payload (~22% of it) to enforce two "this field is required" checks,
// and this file is the only zod consumer in the app. The API shape below
// deliberately mirrors the subset of zod's surface LoginForm.tsx uses —
// safeParse() → { success, data } | { success, error }, and
// error.flatten().fieldErrors — so the call site is unchanged and can
// move back to zod (or forward to a shared validator) without a rewrite.
//
// If a second form ever needs real schema validation, reach for a small
// validator library rather than re-adding zod for one call site.

export type LoginInput = {
  username: string;
  password: string;
};

type FieldErrors = {
  [K in keyof LoginInput]?: string[];
};

type ParseFailure = {
  success: false;
  error: { flatten: () => { fieldErrors: FieldErrors } };
};

type ParseSuccess = {
  success: true;
  data: LoginInput;
};

export type ParseResult = ParseSuccess | ParseFailure;

/** Mirrors the zod object schema this file used to export. */
export const loginSchema = {
  safeParse(input: { username: unknown; password: unknown }): ParseResult {
    const fieldErrors: FieldErrors = {};

    const username = typeof input.username === "string" ? input.username : "";
    const password = typeof input.password === "string" ? input.password : "";

    // Matches the previous z.string().min(1, "required") on both fields.
    // Note: no trim — a password of spaces was valid before and stays so.
    if (username.length < 1) fieldErrors.username = ["required"];
    if (password.length < 1) fieldErrors.password = ["required"];

    if (fieldErrors.username || fieldErrors.password) {
      return {
        success: false,
        error: { flatten: () => ({ fieldErrors }) },
      };
    }
    return { success: true, data: { username, password } };
  },
};
