"use client";

import { useRef, useState } from "react";
import { ApiError } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useLogin } from "../hooks";
import { loginSchema } from "../schemas";

export function LoginForm() {
  const login = useLogin();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  // Per-field validation errors render inline under their input (with
  // aria-invalid so the primitive's error styling kicks in); formError
  // is reserved for server-side outcomes (bad credentials, network).
  const [fieldErrors, setFieldErrors] = useState<{ username?: string; password?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setFormError(null);
    const parsed = loginSchema.safeParse({ username, password });
    if (!parsed.success) {
      const flat = parsed.error.flatten().fieldErrors;
      const errs = {
        username: flat.username?.[0],
        password: flat.password?.[0],
      };
      setFieldErrors(errs);
      if (errs.username) usernameRef.current?.focus();
      else if (errs.password) passwordRef.current?.focus();
      return;
    }
    setFieldErrors({});
    try {
      await login.mutateAsync(parsed.data);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setFormError("invalid credentials");
      } else {
        setFormError("login failed");
      }
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4" noValidate>
      <div className="space-y-1.5">
        <Label htmlFor="username">Username or email</Label>
        <Input
          ref={usernameRef}
          id="username"
          name="username"
          autoComplete="username"
          value={username}
          onChange={(e) => {
            setUsername(e.target.value);
            setFieldErrors((prev) => (prev.username ? { ...prev, username: undefined } : prev));
          }}
          aria-invalid={fieldErrors.username ? true : undefined}
          required
        />
        {fieldErrors.username && (
          <p role="alert" className="font-mono text-xs text-[var(--error)]">
            {fieldErrors.username}
          </p>
        )}
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="password">Password</Label>
        <Input
          ref={passwordRef}
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => {
            setPassword(e.target.value);
            setFieldErrors((prev) => (prev.password ? { ...prev, password: undefined } : prev));
          }}
          aria-invalid={fieldErrors.password ? true : undefined}
          required
        />
        {fieldErrors.password && (
          <p role="alert" className="font-mono text-xs text-[var(--error)]">
            {fieldErrors.password}
          </p>
        )}
      </div>
      {formError && (
        <p role="alert" className="font-mono text-xs text-[var(--error)]">
          {formError}
        </p>
      )}
      <Button type="submit" className="w-full" disabled={login.isPending}>
        {login.isPending ? "signing in…" : "Sign in"}
      </Button>
    </form>
  );
}
