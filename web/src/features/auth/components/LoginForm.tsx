"use client";

import { useState } from "react";
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
  const [errMsg, setErrMsg] = useState<string | null>(null);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setErrMsg(null);
    const parsed = loginSchema.safeParse({ username, password });
    if (!parsed.success) {
      setErrMsg(parsed.error.issues[0]?.message ?? "invalid input");
      return;
    }
    try {
      await login.mutateAsync(parsed.data);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setErrMsg("invalid credentials");
      } else {
        setErrMsg("login failed");
      }
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="username">Username</Label>
        <Input
          id="username"
          name="username"
          autoComplete="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          required
        />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="password">Password</Label>
        <Input
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
      </div>
      {errMsg && (
        <p className="font-mono text-xs text-[oklch(0.577_0.245_27.325)]">
          {errMsg}
        </p>
      )}
      <Button type="submit" className="w-full" disabled={login.isPending}>
        {login.isPending ? "signing in…" : "Sign in"}
      </Button>
    </form>
  );
}
