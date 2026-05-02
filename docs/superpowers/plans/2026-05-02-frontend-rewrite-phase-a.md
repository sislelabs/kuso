# Phase A: web/ scaffold + auth + login flow

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the new Next.js 16 frontend in `web/`, port the robiv0 design tokens + shadcn primitives, build the login page + auth shim, and wire it to the existing Go backend behind `KUSO_FRONTEND=next`.

**Architecture:** Next.js 16 App Router with `output: "export"`, output rsync'd into `server-go/internal/web/dist-next/`, embedded alongside the legacy Vue `dist/` until Phase F. Auth is a thin React Query + React Context shim around the existing Go JWT endpoints, shaped like Better Auth so robiv0 component code ports unmodified.

**Tech Stack:** Next.js 16, React 19, TypeScript strict, Tailwind 4, shadcn/ui (`base-nova`), Base UI primitives, `next-themes`, `@tanstack/react-query`, `lucide-react`, `cmdk`, `sonner`.

**Spec:** `docs/superpowers/specs/2026-05-02-frontend-rewrite-nextjs-design.md` — sections 1, 2, 3, 4, 17 (Phase A row).

---

## File Structure

```
web/
├── package.json                       NEW
├── tsconfig.json                      NEW
├── next.config.ts                     NEW (output: "export", dev rewrites)
├── tailwind.config.ts                 NEW
├── postcss.config.mjs                 NEW
├── components.json                    NEW (shadcn config)
├── eslint.config.mjs                  NEW
├── .gitignore                         NEW
├── public/
│   └── favicon.ico                    NEW (lift from client/public)
└── src/
    ├── app/
    │   ├── globals.css                NEW (tokens + fonts + base)
    │   ├── layout.tsx                 NEW (root: fonts, theme provider, query client, toaster)
    │   ├── not-found.tsx              NEW
    │   ├── (auth)/
    │   │   ├── layout.tsx             NEW (centered card shell)
    │   │   └── login/
    │   │       └── page.tsx           NEW (login form + GitHub button + OAuth2 button)
    │   └── (app)/
    │       ├── layout.tsx             NEW (AuthGate placeholder; full shell in Phase B)
    │       └── page.tsx               NEW (redirects to /projects)
    ├── components/
    │   ├── ui/                        NEW (shadcn primitives ported from robiv0)
    │   │   ├── button.tsx
    │   │   ├── card.tsx
    │   │   ├── input.tsx
    │   │   ├── label.tsx
    │   │   ├── separator.tsx
    │   │   ├── skeleton.tsx
    │   │   ├── tooltip.tsx
    │   │   └── (more added as needed in later phases)
    │   └── shared/
    │       ├── Logo.tsx               NEW
    │       ├── ThemeToggle.tsx        NEW (lifted from robiv0)
    │       └── ErrorBoundary.tsx      NEW
    ├── features/
    │   └── auth/
    │       ├── api.ts                 NEW (login, getSession, signOut)
    │       ├── hooks.ts               NEW (useSession, useLogin, useLogout)
    │       ├── schemas.ts             NEW (zod)
    │       ├── components/
    │       │   ├── AuthGate.tsx       NEW
    │       │   ├── LoginForm.tsx      NEW
    │       │   └── SocialButtons.tsx  NEW
    │       └── index.ts               NEW
    ├── lib/
    │   ├── api-client.ts              NEW (fetch wrapper, JWT injection, ApiError)
    │   ├── utils.ts                   NEW (cn helper from shadcn)
    │   ├── env.ts                     NEW (NEXT_PUBLIC_KUSO_API_URL etc.)
    │   └── query-client.tsx           NEW (QueryClientProvider)
    ├── hooks/
    │   └── (added in later phases)
    └── types/
        └── api.ts                     NEW (mirrors of Go API responses we touch in Phase A)

server-go/internal/web/
└── web.go                             MODIFY (dual embed: dist + dist-next, switch on KUSO_FRONTEND)
└── dist-next/.gitkeep                 NEW (placeholder so embed compiles)

.github/workflows/
└── ci.yml                             MODIFY (add frontend build stage)

Makefile or scripts/build-frontend.sh  NEW (one-liner: cd web && npm ci && npm run build && rsync ...)
```

---

## Task 1: Initialize the Next.js project

**Files:**
- Create: `web/package.json`
- Create: `web/tsconfig.json`
- Create: `web/next.config.ts`
- Create: `web/.gitignore`
- Create: `web/eslint.config.mjs`

- [ ] **Step 1: Create the web/ directory and initialize package.json**

```bash
mkdir -p /Users/sisle/code/work/kubero-setup/kuso/web
cd /Users/sisle/code/work/kubero-setup/kuso/web
```

Write `web/package.json`:

```json
{
  "name": "kuso-web",
  "version": "0.0.1",
  "private": true,
  "license": "GPL-3.0",
  "engines": { "node": ">=20.0.0" },
  "scripts": {
    "dev": "next dev --turbopack -p 3000",
    "build": "next build",
    "start": "next start",
    "lint": "next lint",
    "typecheck": "tsc --noEmit"
  },
  "dependencies": {
    "@base-ui/react": "^1.4.0",
    "@tanstack/react-query": "^5.62.0",
    "class-variance-authority": "^0.7.1",
    "clsx": "^2.1.1",
    "cmdk": "^1.1.1",
    "lucide-react": "^0.469.0",
    "next": "16.2.3",
    "next-themes": "^0.4.6",
    "react": "19.2.4",
    "react-dom": "19.2.4",
    "sonner": "^2.0.7",
    "tailwind-merge": "^3.5.0",
    "tw-animate-css": "^1.4.0",
    "zod": "^4.3.6"
  },
  "devDependencies": {
    "@tailwindcss/postcss": "^4",
    "@types/node": "^20",
    "@types/react": "^19",
    "@types/react-dom": "^19",
    "eslint": "^9",
    "eslint-config-next": "16.2.3",
    "tailwindcss": "^4",
    "typescript": "^5"
  }
}
```

- [ ] **Step 2: Write tsconfig.json**

`web/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["dom", "dom.iterable", "esnext"],
    "allowJs": false,
    "skipLibCheck": true,
    "strict": true,
    "noEmit": true,
    "esModuleInterop": true,
    "module": "esnext",
    "moduleResolution": "bundler",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "jsx": "preserve",
    "incremental": true,
    "plugins": [{ "name": "next" }],
    "paths": { "@/*": ["./src/*"] }
  },
  "include": ["next-env.d.ts", "**/*.ts", "**/*.tsx", ".next/types/**/*.ts"],
  "exclude": ["node_modules"]
}
```

- [ ] **Step 3: Write next.config.ts**

`web/next.config.ts`:

```ts
import type { NextConfig } from "next";

const isDev = process.env.NODE_ENV === "development";

const config: NextConfig = {
  output: "export",
  trailingSlash: false,
  images: { unoptimized: true },
  reactStrictMode: true,
  async rewrites() {
    if (!isDev) return [];
    const apiTarget = process.env.NEXT_PUBLIC_KUSO_API_URL ?? "http://localhost:8080";
    return [
      { source: "/api/:path*", destination: `${apiTarget}/api/:path*` },
      { source: "/ws/:path*", destination: `${apiTarget}/ws/:path*` },
      { source: "/healthz", destination: `${apiTarget}/healthz` },
    ];
  },
};

export default config;
```

- [ ] **Step 4: Write .gitignore and eslint config**

`web/.gitignore`:

```
node_modules
.next
out
.env*.local
*.tsbuildinfo
next-env.d.ts
```

`web/eslint.config.mjs`:

```js
import { FlatCompat } from "@eslint/eslintrc";

const compat = new FlatCompat({ baseDirectory: import.meta.dirname });

export default [...compat.extends("next/core-web-vitals", "next/typescript")];
```

- [ ] **Step 5: Install deps and verify clean install**

Run: `cd /Users/sisle/code/work/kubero-setup/kuso/web && npm install`
Expected: clean install, no peer warnings about React, lockfile produced.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/tsconfig.json web/next.config.ts web/.gitignore web/eslint.config.mjs
git commit -m "feat(web): initialize Next.js 16 project scaffold"
```

---

## Task 2: Tailwind 4 + design tokens + fonts

**Files:**
- Create: `web/postcss.config.mjs`
- Create: `web/tailwind.config.ts`
- Create: `web/src/app/globals.css`
- Create: `web/components.json`
- Create: `web/src/lib/utils.ts`

- [ ] **Step 1: Write postcss config**

`web/postcss.config.mjs`:

```js
export default { plugins: { "@tailwindcss/postcss": {} } };
```

- [ ] **Step 2: Write tailwind config (Tailwind 4 uses CSS-first; this stays minimal)**

`web/tailwind.config.ts`:

```ts
import type { Config } from "tailwindcss";

const config: Config = {
  content: ["./src/**/*.{ts,tsx}"],
  theme: { extend: {} },
};

export default config;
```

- [ ] **Step 3: Write globals.css with kuso tokens (violet accent in dark mode)**

`web/src/app/globals.css`:

```css
@import "tailwindcss";
@import "tw-animate-css";

@custom-variant dark (&:is(.dark *));

@theme inline {
  --color-background: var(--background);
  --color-foreground: var(--foreground);
  --font-sans: var(--font-roboto);
  --font-mono: var(--font-geist-mono);
  --font-heading: var(--font-dm-sans);
  --color-sidebar-ring: var(--sidebar-ring);
  --color-sidebar-border: var(--sidebar-border);
  --color-sidebar-accent-foreground: var(--sidebar-accent-foreground);
  --color-sidebar-accent: var(--sidebar-accent);
  --color-sidebar-primary-foreground: var(--sidebar-primary-foreground);
  --color-sidebar-primary: var(--sidebar-primary);
  --color-sidebar-foreground: var(--sidebar-foreground);
  --color-sidebar: var(--sidebar);
  --color-chart-5: var(--chart-5);
  --color-chart-4: var(--chart-4);
  --color-chart-3: var(--chart-3);
  --color-chart-2: var(--chart-2);
  --color-chart-1: var(--chart-1);
  --color-ring: var(--ring);
  --color-input: var(--input);
  --color-border: var(--border);
  --color-destructive: var(--destructive);
  --color-accent-foreground: var(--accent-foreground);
  --color-accent: var(--accent-ui);
  --color-muted-foreground: var(--muted-foreground);
  --color-muted: var(--muted);
  --color-secondary-foreground: var(--secondary-foreground);
  --color-secondary: var(--secondary);
  --color-primary-foreground: var(--primary-foreground);
  --color-primary: var(--primary);
  --color-popover-foreground: var(--popover-foreground);
  --color-popover: var(--popover);
  --color-card-foreground: var(--card-foreground);
  --color-card: var(--card);
  --radius-sm: calc(var(--radius) * 0.6);
  --radius-md: calc(var(--radius) * 0.8);
  --radius-lg: var(--radius);
  --radius-xl: calc(var(--radius) * 1.4);
}

:root {
  --bg-primary: #FFFFFF;
  --bg-secondary: #F7F7F8;
  --bg-tertiary: #EFEFF1;
  --bg-elevated: #FFFFFF;
  --bg-inverse: #111114;
  --text-primary: #111114;
  --text-secondary: #555560;
  --text-tertiary: #8A8A93;
  --text-inverse: #FFFFFF;
  --accent: #111114;
  --accent-hover: #2A2A30;
  --accent-subtle: #F4F4F5;
  --border-default: #E4E4E7;
  --border-subtle: #EFEFF1;
  --shadow-sm: 0 1px 2px rgba(17, 17, 20, 0.04);
  --shadow-md: 0 4px 12px rgba(17, 17, 20, 0.06);
  --shadow-lg: 0 12px 40px rgba(17, 17, 20, 0.08);

  --background: #FFFFFF;
  --foreground: #111114;
  --card: #FFFFFF;
  --card-foreground: #111114;
  --popover: #FFFFFF;
  --popover-foreground: #111114;
  --primary: #111114;
  --primary-foreground: #FFFFFF;
  --secondary: #F7F7F8;
  --secondary-foreground: #111114;
  --muted: #EFEFF1;
  --muted-foreground: #555560;
  --accent-ui: #111114;
  --accent-foreground: #FFFFFF;
  --destructive: oklch(0.577 0.245 27.325);
  --border: #E4E4E7;
  --input: #EFEFF1;
  --ring: #111114;
  --radius: 0.25rem;

  --sidebar: #FFFFFF;
  --sidebar-foreground: #111114;
  --sidebar-primary: #111114;
  --sidebar-primary-foreground: #FFFFFF;
  --sidebar-accent: #F4F4F5;
  --sidebar-accent-foreground: #111114;
  --sidebar-border: #E4E4E7;
  --sidebar-ring: #111114;

  --chart-1: #111114;
  --chart-2: #555560;
  --chart-3: #8A8A93;
  --chart-4: #111114;
  --chart-5: #E4E4E7;
}

.dark {
  --bg-primary: #111110;
  --bg-secondary: #191918;
  --bg-tertiary: #222221;
  --bg-elevated: #2A2A28;
  --bg-inverse: #FAFAF9;
  --text-primary: #EDEDEB;
  --text-secondary: #A0A09A;
  --text-tertiary: #6B6B63;
  --text-inverse: #111110;
  --accent: #7C5CFF;
  --accent-hover: #6845F0;
  --accent-subtle: rgba(124, 92, 255, 0.1);
  --border-default: #2A2A28;
  --border-subtle: #222221;
  --shadow-sm: 0 1px 2px rgba(0, 0, 0, 0.2);
  --shadow-md: 0 4px 12px rgba(0, 0, 0, 0.3);
  --shadow-lg: 0 12px 40px rgba(0, 0, 0, 0.4);

  --background: #111110;
  --foreground: #EDEDEB;
  --card: #2A2A28;
  --card-foreground: #EDEDEB;
  --popover: #2A2A28;
  --popover-foreground: #EDEDEB;
  --primary: #7C5CFF;
  --primary-foreground: #FFFFFF;
  --secondary: #191918;
  --secondary-foreground: #EDEDEB;
  --muted: #222221;
  --muted-foreground: #A0A09A;
  --accent-ui: #222221;
  --accent-foreground: #EDEDEB;
  --destructive: oklch(0.704 0.191 22.216);
  --border: #2A2A28;
  --input: #222221;
  --ring: #7C5CFF;

  --sidebar: #191918;
  --sidebar-foreground: #EDEDEB;
  --sidebar-primary: #7C5CFF;
  --sidebar-primary-foreground: #FFFFFF;
  --sidebar-accent: #222221;
  --sidebar-accent-foreground: #EDEDEB;
  --sidebar-border: #2A2A28;
  --sidebar-ring: #7C5CFF;

  --chart-1: #7C5CFF;
  --chart-2: #A0A09A;
  --chart-3: #6B6B63;
  --chart-4: #6845F0;
  --chart-5: #EDEDEB;
}

@layer base {
  * { @apply border-border outline-ring/50; }
  body { @apply bg-background text-foreground; }
  html { @apply font-sans; }
  button, a, input, textarea, select, [data-slot] {
    transition-timing-function: cubic-bezier(0.33, 1, 0.68, 1);
  }
}

.text-hero {
  font-size: clamp(3rem, 6vw, 5.5rem);
  line-height: 1.02;
  letter-spacing: -0.035em;
  font-family: var(--font-dm-sans);
  font-weight: 600;
}

.text-section-heading {
  font-size: clamp(1.75rem, 3vw, 2.5rem);
  line-height: 1.15;
  letter-spacing: -0.025em;
  font-family: var(--font-dm-sans);
  font-weight: 600;
}
```

- [ ] **Step 4: Write components.json (shadcn config)**

`web/components.json`:

```json
{
  "$schema": "https://ui.shadcn.com/schema.json",
  "style": "base-nova",
  "rsc": true,
  "tsx": true,
  "tailwind": {
    "config": "",
    "css": "src/app/globals.css",
    "baseColor": "neutral",
    "cssVariables": true,
    "prefix": ""
  },
  "iconLibrary": "lucide",
  "aliases": {
    "components": "@/components",
    "utils": "@/lib/utils",
    "ui": "@/components/ui",
    "lib": "@/lib",
    "hooks": "@/hooks"
  }
}
```

- [ ] **Step 5: Write the cn helper**

`web/src/lib/utils.ts`:

```ts
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
```

- [ ] **Step 6: Commit**

```bash
git add web/postcss.config.mjs web/tailwind.config.ts web/src/app/globals.css web/components.json web/src/lib/utils.ts
git commit -m "feat(web): tailwind 4 + design tokens (violet dark accent) + cn helper"
```

---

## Task 3: Root layout + theme provider + query client

**Files:**
- Create: `web/src/app/layout.tsx`
- Create: `web/src/app/not-found.tsx`
- Create: `web/src/lib/query-client.tsx`
- Create: `web/src/components/shared/ThemeToggle.tsx`

- [ ] **Step 1: Write the root layout**

`web/src/app/layout.tsx`:

```tsx
import type { Metadata } from "next";
import { Roboto, DM_Sans, Geist_Mono } from "next/font/google";
import { ThemeProvider } from "next-themes";
import { Toaster } from "sonner";
import { QueryProvider } from "@/lib/query-client";
import "./globals.css";

const roboto = Roboto({ weight: ["400", "500", "700"], subsets: ["latin"], variable: "--font-roboto", display: "swap" });
const dmSans = DM_Sans({ subsets: ["latin"], variable: "--font-dm-sans", display: "swap" });
const geistMono = Geist_Mono({ subsets: ["latin"], variable: "--font-geist-mono", display: "swap" });

export const metadata: Metadata = {
  title: "kuso",
  description: "Self-hosted, agent-native PaaS for indie developers.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning className={`${roboto.variable} ${dmSans.variable} ${geistMono.variable}`}>
      <body className="font-sans antialiased">
        <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
          <QueryProvider>
            {children}
            <Toaster position="bottom-right" />
          </QueryProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
```

- [ ] **Step 2: Write the query client provider**

`web/src/lib/query-client.tsx`:

```tsx
"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";

export function QueryProvider({ children }: { children: React.ReactNode }) {
  const [client] = useState(() => new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: true,
        retry: (failureCount, err) => {
          if (err && typeof err === "object" && "status" in err) {
            const s = (err as { status: number }).status;
            if (s === 401 || s === 403 || s === 404) return false;
          }
          return failureCount < 2;
        },
      },
    },
  }));
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
```

- [ ] **Step 3: Write the theme toggle component**

`web/src/components/shared/ThemeToggle.tsx`:

```tsx
"use client";

import { useTheme } from "next-themes";
import { Moon, Sun, Monitor } from "lucide-react";
import { useEffect, useState } from "react";

export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  if (!mounted) return <button className="h-8 w-8" aria-hidden />;
  const cycle = () => setTheme(theme === "light" ? "dark" : theme === "dark" ? "system" : "light");
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  return (
    <button
      onClick={cycle}
      aria-label="Toggle theme"
      className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Icon className="h-4 w-4" />
    </button>
  );
}
```

- [ ] **Step 4: Write not-found**

`web/src/app/not-found.tsx`:

```tsx
import Link from "next/link";

export default function NotFound() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--bg-secondary)]">
      <div className="text-center">
        <p className="font-mono text-xs uppercase tracking-widest text-[var(--text-tertiary)]">404</p>
        <h1 className="mt-2 text-section-heading">page not found</h1>
        <Link href="/" className="mt-4 inline-block text-sm text-[var(--text-secondary)] underline">
          back home
        </Link>
      </div>
    </div>
  );
}
```

- [ ] **Step 5: Verify dev server boots**

Run: `cd web && npm run dev`
Expected: server boots on `:3000`, visiting `/` shows the not-found page (no app routes yet), no hydration errors in browser console.
Stop server with Ctrl+C.

- [ ] **Step 6: Commit**

```bash
git add web/src/app/layout.tsx web/src/app/not-found.tsx web/src/lib/query-client.tsx web/src/components/shared/ThemeToggle.tsx
git commit -m "feat(web): root layout with fonts, theme provider, query client, toaster"
```

---

## Task 4: API client + types + env helper

**Files:**
- Create: `web/src/lib/env.ts`
- Create: `web/src/lib/api-client.ts`
- Create: `web/src/types/api.ts`

- [ ] **Step 1: Write env helper**

`web/src/lib/env.ts`:

```ts
export const env = {
  // In dev, requests go through Next's rewrites (proxied to :8080).
  // In prod (static export embedded in Go binary), the API is same-origin.
  apiBase: typeof window === "undefined" ? "" : "",
};
```

- [ ] **Step 2: Write api-client.ts**

`web/src/lib/api-client.ts`:

```ts
import { env } from "./env";

const JWT_KEY = "kuso.jwt";

export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, body: unknown, message: string) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

export function getJwt(): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(JWT_KEY);
}

export function setJwt(token: string) {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(JWT_KEY, token);
  // Also set cookie so server-side guards (if any) and the legacy code path see it.
  document.cookie = `kuso.JWT_TOKEN=${token}; path=/; SameSite=Lax`;
}

export function clearJwt() {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(JWT_KEY);
  document.cookie = "kuso.JWT_TOKEN=; path=/; max-age=0; SameSite=Lax";
}

type Options = Omit<RequestInit, "body"> & { body?: unknown };

export async function api<T>(path: string, opts: Options = {}): Promise<T> {
  const headers = new Headers(opts.headers);
  const jwt = getJwt();
  if (jwt) headers.set("Authorization", `Bearer ${jwt}`);
  if (opts.body !== undefined && !(opts.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${env.apiBase}${path}`, {
    ...opts,
    headers,
    body:
      opts.body === undefined
        ? undefined
        : opts.body instanceof FormData
          ? opts.body
          : JSON.stringify(opts.body),
    credentials: "include",
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let parsed: unknown = undefined;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, parsed, `${res.status} ${res.statusText}`);
  }
  return parsed as T;
}
```

- [ ] **Step 3: Write Phase A types**

`web/src/types/api.ts`:

```ts
export interface AuthMethods {
  local: boolean;
  github: boolean;
  oauth2: boolean;
}

export interface AuthSession {
  isAuthenticated: boolean;
  userId: string;
  username: string;
  role: string;
  userGroups: string[];
  permissions: string[];
  adminDisabled: boolean;
  templatesEnabled: boolean;
  consoleEnabled: boolean;
  metricsEnabled: boolean;
  sleepEnabled: boolean;
  auditEnabled: boolean;
  buildPipeline: boolean;
}

export interface LoginResponse {
  access_token: string;
}

export interface UserProfile {
  id: string;
  username: string;
  email: string;
  firstName: string | null;
  lastName: string | null;
  role: string;
  userGroups: string[];
  permissions: string[];
  image?: string | null;
}
```

- [ ] **Step 4: Commit**

```bash
git add web/src/lib/env.ts web/src/lib/api-client.ts web/src/types/api.ts
git commit -m "feat(web): api-client wrapper with JWT injection and ApiError"
```

---

## Task 5: shadcn primitives — button, input, label, card, separator, skeleton, tooltip

**Files:**
- Create: `web/src/components/ui/button.tsx`
- Create: `web/src/components/ui/input.tsx`
- Create: `web/src/components/ui/label.tsx`
- Create: `web/src/components/ui/card.tsx`
- Create: `web/src/components/ui/separator.tsx`
- Create: `web/src/components/ui/skeleton.tsx`
- Create: `web/src/components/ui/tooltip.tsx`

These ports are lifted directly from `/Users/sisle/code/work/robiv0/src/components/ui/`. They use `@base-ui/react` primitives — verify each file imports compile cleanly before moving on.

- [ ] **Step 1: Port button.tsx verbatim from robiv0**

```bash
cp /Users/sisle/code/work/robiv0/src/components/ui/button.tsx \
   /Users/sisle/code/work/kubero-setup/kuso/web/src/components/ui/button.tsx
```

- [ ] **Step 2: Port input.tsx, label.tsx, card.tsx, separator.tsx, skeleton.tsx, tooltip.tsx**

```bash
for f in input label card separator skeleton tooltip; do
  cp /Users/sisle/code/work/robiv0/src/components/ui/$f.tsx \
     /Users/sisle/code/work/kubero-setup/kuso/web/src/components/ui/$f.tsx
done
```

- [ ] **Step 3: Verify every file uses the @/lib/utils alias and compiles**

Run: `cd web && npm run typecheck`
Expected: no errors. If a file references a primitive we haven't ported (e.g. button needs `Slot` from somewhere), open the offending file and either port the dependency from robiv0 or replace the import per shadcn's vendored primitive convention.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/ui/
git commit -m "feat(web): port shadcn primitives (button, input, label, card, separator, skeleton, tooltip)"
```

---

## Task 6: Logo + ErrorBoundary shared components

**Files:**
- Create: `web/src/components/shared/Logo.tsx`
- Create: `web/src/components/shared/ErrorBoundary.tsx`
- Create: `web/public/favicon.ico` (lifted from existing `client/public/`)

- [ ] **Step 1: Lift the favicon**

```bash
cp /Users/sisle/code/work/kubero-setup/kuso/client/public/favicon.ico \
   /Users/sisle/code/work/kubero-setup/kuso/web/public/favicon.ico 2>/dev/null \
   || echo "no existing favicon, skip"
```

- [ ] **Step 2: Write Logo.tsx**

`web/src/components/shared/Logo.tsx`:

```tsx
import { cn } from "@/lib/utils";

export function Logo({ showText = true, className }: { showText?: boolean; className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <span
        aria-hidden
        className="inline-block h-6 w-6 rounded-md bg-[var(--accent)]"
        style={{ background: "linear-gradient(135deg, var(--accent), var(--accent-hover))" }}
      />
      {showText && (
        <span className="font-heading text-base font-semibold tracking-tight text-[var(--text-primary)]">
          kuso
        </span>
      )}
    </span>
  );
}
```

- [ ] **Step 3: Write ErrorBoundary.tsx**

`web/src/components/shared/ErrorBoundary.tsx`:

```tsx
"use client";

import { Component, type ReactNode } from "react";

interface Props { children: ReactNode; fallback?: ReactNode }
interface State { error: Error | null }

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: unknown) {
    console.error("ErrorBoundary caught:", error, info);
  }

  render() {
    if (this.state.error) {
      return this.props.fallback ?? (
        <div className="p-8 text-center text-sm text-[var(--text-secondary)]">
          <p className="font-mono">something broke</p>
          <p className="mt-2 text-xs">{this.state.error.message}</p>
        </div>
      );
    }
    return this.props.children;
  }
}
```

- [ ] **Step 4: Commit**

```bash
git add web/src/components/shared/Logo.tsx web/src/components/shared/ErrorBoundary.tsx web/public/
git commit -m "feat(web): Logo and ErrorBoundary shared components"
```

---

## Task 7: auth feature — api.ts, schemas.ts, hooks.ts

**Files:**
- Create: `web/src/features/auth/api.ts`
- Create: `web/src/features/auth/schemas.ts`
- Create: `web/src/features/auth/hooks.ts`
- Create: `web/src/features/auth/index.ts`

- [ ] **Step 1: Write schemas.ts**

`web/src/features/auth/schemas.ts`:

```ts
import { z } from "zod";

export const loginSchema = z.object({
  username: z.string().min(1, "required"),
  password: z.string().min(1, "required"),
});

export type LoginInput = z.infer<typeof loginSchema>;
```

- [ ] **Step 2: Write api.ts**

`web/src/features/auth/api.ts`:

```ts
import { api } from "@/lib/api-client";
import type { AuthMethods, AuthSession, LoginResponse, UserProfile } from "@/types/api";
import type { LoginInput } from "./schemas";

export async function getAuthMethods(): Promise<AuthMethods> {
  return api<AuthMethods>("/api/auth/methods");
}

export async function login(input: LoginInput): Promise<LoginResponse> {
  return api<LoginResponse>("/api/auth/login", { method: "POST", body: input });
}

export async function getSession(): Promise<AuthSession> {
  return api<AuthSession>("/api/auth/session");
}

export async function getProfile(): Promise<UserProfile> {
  return api<UserProfile>("/api/users/profile");
}
```

- [ ] **Step 3: Write hooks.ts (the useSession shim shaped like Better Auth)**

`web/src/features/auth/hooks.ts`:

```ts
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { ApiError, clearJwt, setJwt } from "@/lib/api-client";
import { getAuthMethods, getProfile, getSession, login as loginApi } from "./api";
import type { LoginInput } from "./schemas";

export const sessionQueryKey = ["auth", "session"] as const;

export interface SessionShape {
  user: {
    id: string;
    name: string;
    email: string;
    image: string | null;
    role: string;
  };
  session: {
    permissions: string[];
    userGroups: string[];
  };
}

export function useSession() {
  return useQuery<SessionShape | null>({
    queryKey: sessionQueryKey,
    queryFn: async () => {
      try {
        const [s, p] = await Promise.all([getSession(), getProfile()]);
        if (!s.isAuthenticated) return null;
        const fullName = [p.firstName, p.lastName].filter(Boolean).join(" ");
        return {
          user: {
            id: p.id,
            name: fullName || p.username,
            email: p.email,
            image: p.image ?? null,
            role: p.role,
          },
          session: {
            permissions: p.permissions ?? [],
            userGroups: p.userGroups ?? [],
          },
        };
      } catch (e) {
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) return null;
        throw e;
      }
    },
    staleTime: 60_000,
  });
}

export function useAuthMethods() {
  return useQuery({ queryKey: ["auth", "methods"], queryFn: getAuthMethods, staleTime: 5 * 60_000 });
}

export function useLogin() {
  const qc = useQueryClient();
  const router = useRouter();
  return useMutation({
    mutationFn: (input: LoginInput) => loginApi(input),
    onSuccess: async (data) => {
      setJwt(data.access_token);
      await qc.invalidateQueries({ queryKey: sessionQueryKey });
      const url = new URL(window.location.href);
      const next = url.searchParams.get("next") ?? "/projects";
      router.replace(next);
    },
  });
}

export function useSignOut() {
  const qc = useQueryClient();
  const router = useRouter();
  return () => {
    clearJwt();
    qc.removeQueries({ queryKey: sessionQueryKey });
    router.replace("/login");
  };
}
```

- [ ] **Step 4: Write index.ts**

`web/src/features/auth/index.ts`:

```ts
export { useSession, useAuthMethods, useLogin, useSignOut, sessionQueryKey } from "./hooks";
export type { SessionShape } from "./hooks";
export { loginSchema } from "./schemas";
export type { LoginInput } from "./schemas";
```

- [ ] **Step 5: Typecheck**

Run: `cd web && npm run typecheck`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/auth/
git commit -m "feat(web): auth feature — api, schemas, useSession/useLogin/useSignOut hooks"
```

---

## Task 8: AuthGate component + (app) layout placeholder

**Files:**
- Create: `web/src/features/auth/components/AuthGate.tsx`
- Create: `web/src/app/(app)/layout.tsx`
- Create: `web/src/app/(app)/page.tsx`

- [ ] **Step 1: Write AuthGate.tsx**

`web/src/features/auth/components/AuthGate.tsx`:

```tsx
"use client";

import { usePathname, useRouter } from "next/navigation";
import { useEffect } from "react";
import { Skeleton } from "@/components/ui/skeleton";
import { useSession } from "../hooks";

export function AuthGate({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const { data, isPending, isError } = useSession();

  useEffect(() => {
    if (isPending) return;
    if (data === null || isError) {
      const next = encodeURIComponent(pathname);
      router.replace(`/login?next=${next}`);
    }
  }, [data, isPending, isError, pathname, router]);

  if (isPending) {
    return (
      <div className="flex h-screen">
        <div className="hidden w-[260px] border-r border-[var(--border-subtle)] bg-[var(--bg-secondary)] lg:block">
          <div className="space-y-3 p-4">
            <Skeleton className="h-8 w-32" />
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-6 w-3/4" />
          </div>
        </div>
        <div className="flex-1 p-8">
          <Skeleton className="mb-4 h-8 w-48" />
          <Skeleton className="h-32 w-full" />
        </div>
      </div>
    );
  }

  if (data === null || isError) return null;

  return <>{children}</>;
}
```

- [ ] **Step 2: Write (app)/layout.tsx (placeholder; full shell in Phase B)**

`web/src/app/(app)/layout.tsx`:

```tsx
import { AuthGate } from "@/features/auth/components/AuthGate";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  return <AuthGate>{children}</AuthGate>;
}
```

- [ ] **Step 3: Write (app)/page.tsx — redirects to /projects**

`web/src/app/(app)/page.tsx`:

```tsx
"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export default function AppIndex() {
  const router = useRouter();
  useEffect(() => { router.replace("/projects"); }, [router]);
  return null;
}
```

- [ ] **Step 4: Commit**

```bash
git add web/src/features/auth/components/AuthGate.tsx web/src/app/\(app\)/
git commit -m "feat(web): AuthGate component and (app) layout"
```

---

## Task 9: Login page + LoginForm + SocialButtons

**Files:**
- Create: `web/src/app/(auth)/layout.tsx`
- Create: `web/src/app/(auth)/login/page.tsx`
- Create: `web/src/features/auth/components/LoginForm.tsx`
- Create: `web/src/features/auth/components/SocialButtons.tsx`

- [ ] **Step 1: Write the auth shell layout**

`web/src/app/(auth)/layout.tsx`:

```tsx
import { Logo } from "@/components/shared/Logo";
import { ThemeToggle } from "@/components/shared/ThemeToggle";

export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="relative flex min-h-screen items-center justify-center bg-[var(--bg-secondary)] px-4">
      <div className="absolute right-4 top-4">
        <ThemeToggle />
      </div>
      <div className="w-full max-w-sm space-y-6">
        <div className="flex justify-center">
          <Logo />
        </div>
        <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-6 shadow-[var(--shadow-md)]">
          {children}
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Write LoginForm.tsx**

`web/src/features/auth/components/LoginForm.tsx`:

```tsx
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
      if (e instanceof ApiError && e.status === 401) setErrMsg("invalid credentials");
      else setErrMsg("login failed");
    }
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="username">Username</Label>
        <Input id="username" name="username" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="password">Password</Label>
        <Input id="password" name="password" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
      </div>
      {errMsg && <p className="font-mono text-xs text-[oklch(0.577_0.245_27.325)]">{errMsg}</p>}
      <Button type="submit" className="w-full" disabled={login.isPending}>
        {login.isPending ? "signing in…" : "Sign in"}
      </Button>
    </form>
  );
}
```

- [ ] **Step 3: Write SocialButtons.tsx**

`web/src/features/auth/components/SocialButtons.tsx`:

```tsx
"use client";

import { Github } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuthMethods } from "../hooks";

export function SocialButtons() {
  const { data } = useAuthMethods();
  if (!data) return null;
  if (!data.github && !data.oauth2) return null;
  return (
    <div className="space-y-2">
      {data.github && (
        <Button
          variant="outline"
          className="w-full"
          onClick={() => { window.location.href = "/api/auth/github"; }}
          type="button"
        >
          <Github className="h-4 w-4" />
          Continue with GitHub
        </Button>
      )}
      {data.oauth2 && (
        <Button
          variant="outline"
          className="w-full"
          onClick={() => { window.location.href = "/api/auth/oauth2"; }}
          type="button"
        >
          Continue with SSO
        </Button>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Write the login page**

`web/src/app/(auth)/login/page.tsx`:

```tsx
import { Separator } from "@/components/ui/separator";
import { LoginForm } from "@/features/auth/components/LoginForm";
import { SocialButtons } from "@/features/auth/components/SocialButtons";

export default function LoginPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="font-heading text-xl font-semibold tracking-tight">Sign in</h1>
        <p className="text-sm text-[var(--text-secondary)]">to your kuso instance</p>
      </div>
      <LoginForm />
      <div className="flex items-center gap-2">
        <Separator className="flex-1" />
        <span className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">or</span>
        <Separator className="flex-1" />
      </div>
      <SocialButtons />
    </div>
  );
}
```

- [ ] **Step 5: Manual smoke test in dev**

Run two terminals:

```bash
# Terminal 1
cd /Users/sisle/code/work/kubero-setup/kuso/server-go && go run ./cmd/kuso-server

# Terminal 2
cd /Users/sisle/code/work/kubero-setup/kuso/web && KUSO_DEV_CORS=1 npm run dev
```

Visit `http://localhost:3000/login`. Expected: login form renders with kuso violet accent in dark mode (toggle theme to verify), GitHub button visible iff backend has `GITHUB_CLIENT_*` configured. Submit local creds (use whatever was seeded in your dev DB) → token lands in localStorage → redirected to `/projects` (which 404s for now — that's Phase B).

Inspect localStorage in devtools: `kuso.jwt` should hold a JWT.

- [ ] **Step 6: Commit**

```bash
git add web/src/app/\(auth\)/ web/src/features/auth/components/LoginForm.tsx web/src/features/auth/components/SocialButtons.tsx
git commit -m "feat(web): login page with local + GitHub + OAuth2 sign-in"
```

---

## Task 10: Static export + dual embed in Go server

**Files:**
- Create: `web/src/app/(app)/projects/page.tsx` (placeholder, prevents 404 when redirected)
- Modify: `server-go/internal/web/web.go`
- Create: `server-go/internal/web/dist-next/.gitkeep`
- Create: `scripts/build-frontend.sh`

- [ ] **Step 1: Add a placeholder /projects page (real one in Phase B)**

`web/src/app/(app)/projects/page.tsx`:

```tsx
"use client";

import { useSession, useSignOut } from "@/features/auth";

export default function ProjectsPlaceholder() {
  const { data } = useSession();
  const signOut = useSignOut();
  return (
    <div className="p-8">
      <h1 className="font-heading text-2xl font-semibold">Projects</h1>
      <p className="mt-2 text-sm text-[var(--text-secondary)]">
        Welcome, {data?.user.name ?? "user"}. Phase B will replace this placeholder.
      </p>
      <button onClick={signOut} className="mt-4 text-sm underline">sign out</button>
    </div>
  );
}
```

- [ ] **Step 2: Run a static build**

```bash
cd /Users/sisle/code/work/kubero-setup/kuso/web
npm run build
ls out/
```

Expected: `out/` directory exists with `index.html`, `_next/`, `login.html` (or `login/index.html` depending on trailingSlash), `404.html`. If the build fails on a route requiring server features, fix that route to be fully static (no `cookies()`, `headers()`, `server-only` imports, etc.) before continuing.

- [ ] **Step 3: Create dist-next directory in server-go**

```bash
mkdir -p /Users/sisle/code/work/kubero-setup/kuso/server-go/internal/web/dist-next
echo "Placeholder — replaced by web/out/ at build time" > /Users/sisle/code/work/kubero-setup/kuso/server-go/internal/web/dist-next/.gitkeep
```

- [ ] **Step 4: Modify web.go for dual-embed**

Read current contents to verify:

```bash
cat /Users/sisle/code/work/kubero-setup/kuso/server-go/internal/web/web.go
```

Replace with:

```go
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed dist
var distLegacyFS embed.FS

//go:embed dist-next
var distNextFS embed.FS

// Dist returns the embedded SPA bundle. Defaults to the legacy Vue dist.
// Set KUSO_FRONTEND=next to serve the Next.js build instead.
func Dist() (fs.FS, error) {
	if os.Getenv("KUSO_FRONTEND") == "next" {
		return fs.Sub(distNextFS, "dist-next")
	}
	return fs.Sub(distLegacyFS, "dist")
}
```

- [ ] **Step 5: Write the build helper script**

`scripts/build-frontend.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/web"

echo "==> installing web/ deps"
npm ci --no-audit --no-fund

echo "==> building web/ (next export)"
npm run build

echo "==> syncing web/out -> server-go/internal/web/dist-next"
rm -rf "$ROOT/server-go/internal/web/dist-next"
mkdir -p "$ROOT/server-go/internal/web/dist-next"
cp -R "$ROOT/web/out/." "$ROOT/server-go/internal/web/dist-next/"

echo "==> done"
```

```bash
chmod +x /Users/sisle/code/work/kubero-setup/kuso/scripts/build-frontend.sh
```

- [ ] **Step 6: Build the Go server with the new frontend embedded**

```bash
cd /Users/sisle/code/work/kubero-setup/kuso
./scripts/build-frontend.sh
cd server-go
go build -o /tmp/kuso-server ./cmd/kuso-server
```

Expected: clean build, no compile errors on the dual-embed.

- [ ] **Step 7: Smoke-run with KUSO_FRONTEND=next**

```bash
KUSO_FRONTEND=next /tmp/kuso-server &
SERVER_PID=$!
sleep 1
curl -sI http://localhost:8080/login | head -3
curl -sI http://localhost:8080/api/status | head -3
kill $SERVER_PID
```

Expected: `200 OK` on `/login` (Next bundle), `200 OK` on `/api/status` (Go API).

Then verify the legacy path still works:

```bash
/tmp/kuso-server &
SERVER_PID=$!
sleep 1
curl -sI http://localhost:8080/ | head -3
kill $SERVER_PID
```

Expected: `200 OK` and the response is the Vue index.html (different content from the Next one).

- [ ] **Step 8: Commit**

```bash
git add web/src/app/\(app\)/projects/page.tsx server-go/internal/web/web.go server-go/internal/web/dist-next/.gitkeep scripts/build-frontend.sh
git commit -m "feat(server-go): dual-embed legacy + next dists, KUSO_FRONTEND switch"
```

Note on `.gitignore`: `server-go/internal/web/dist-next/` content other than `.gitkeep` is build-time output and should be gitignored. Add to repo root `.gitignore`:

```
server-go/internal/web/dist-next/*
!server-go/internal/web/dist-next/.gitkeep
```

```bash
git add .gitignore
git commit -m "chore: gitignore dist-next build output"
```

---

## Task 11: CI workflow — frontend build stage

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Read the current CI workflow**

```bash
cat /Users/sisle/code/work/kubero-setup/kuso/.github/workflows/ci.yml
```

- [ ] **Step 2: Add a frontend build step before the Go build**

Locate the job that runs `go build`. Above it, add:

```yaml
      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: "20"
          cache: "npm"
          cache-dependency-path: web/package-lock.json

      - name: Build frontend (web/)
        run: ./scripts/build-frontend.sh
```

If the existing job builds `server-go` from a working directory other than repo root, ensure `./scripts/build-frontend.sh` runs from repo root (the script handles this internally via `BASH_SOURCE`-relative path).

- [ ] **Step 3: Push and verify CI passes**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add web/ frontend build stage before go build"
git push
```

Watch the CI run on GitHub. Expected: green. The Go binary built in CI now has both Vue and Next dists embedded.

---

## Task 12: README + docs note

**Files:**
- Modify: `README.md`
- Create: `web/README.md`

- [ ] **Step 1: Update root README**

Read the current README:

```bash
cat /Users/sisle/code/work/kubero-setup/kuso/README.md
```

In the "Repo layout" table, add a row above `client/`:

```
| `web/`       | Next.js 16 frontend (target). Built into `server-go/internal/web/dist-next`. |
```

And update the `client/` row to mark it as "(legacy, retired in Phase F)".

- [ ] **Step 2: Write web/README.md**

`web/README.md`:

```markdown
# kuso web

Next.js 16 frontend for kuso. Static export embedded into the Go server.

## Dev

```bash
# Terminal 1: backend
cd ../server-go && go run ./cmd/kuso-server

# Terminal 2: frontend (proxies /api and /ws to :8080)
cd web && npm run dev
```

Open http://localhost:3000.

## Build

`./scripts/build-frontend.sh` from repo root. Output goes to `server-go/internal/web/dist-next/`. The Go server serves it when `KUSO_FRONTEND=next`.

## Stack

- Next.js 16 + React 19 (App Router, static export)
- Tailwind 4 + shadcn/ui (`base-nova`) + Base UI
- TanStack Query for server state
- Visual reference: [biznesguys/robiv0](https://github.com/biznesguys/robiv0)
```

- [ ] **Step 3: Commit**

```bash
git add README.md web/README.md
git commit -m "docs: README updates for web/ frontend"
```

---

## Phase A done

After Task 12, you have:

- A Next.js 16 project at `web/` with the robiv0 design system tokens
- shadcn primitives ported (button, input, label, card, separator, skeleton, tooltip)
- A working `useSession()` shim shaped like Better Auth
- A login page handling local + GitHub + OAuth2
- AuthGate wrapping the (app) layout
- Static export embedded in the Go binary alongside the legacy Vue dist
- `KUSO_FRONTEND=next` flag flips the served frontend
- CI builds both
- Vue app still works untouched

**Smoke test before moving to Phase B:**

```bash
./scripts/build-frontend.sh
cd server-go && go build -o /tmp/kuso-server ./cmd/kuso-server
KUSO_FRONTEND=next /tmp/kuso-server &
sleep 2
# Visit http://localhost:8080/login in browser, sign in, get redirected to /projects placeholder.
# Sign out works. Theme toggle works. localStorage shows kuso.jwt cleared on signout.
```

If that works end-to-end, Phase A is done. Move to Phase B.

---

## Self-review

**Spec coverage check** — every Phase A spec requirement has a task:

| Spec | Task |
|---|---|
| § 1.1 directory layout (web/) | Task 1, 6, 8, 9 |
| § 1.2 build sequence + rsync | Task 10 step 5 (build-frontend.sh) |
| § 1.3 dev rewrites | Task 1 step 3 (next.config.ts) |
| § 2.1 tokens (kuso violet) | Task 2 step 3 |
| § 2.2 fonts | Task 3 step 1 |
| § 2.3 shadcn primitives | Task 5 |
| § 2.5 theme toggle | Task 3 step 3 |
| § 3.1 useSession shim | Task 7 step 3 |
| § 3.2 AuthGate | Task 8 step 1 |
| § 3.3 sign-in flows (local, GitHub, OAuth2) | Task 9 |
| § 3.4 api-client.ts | Task 4 step 2 |
| § 4.2 auth shell | Task 9 step 1 |
| § 17 Phase A row | Task 1–12 |
| § 1.1 dual-embed mechanism | Task 10 step 4 |

**Placeholder scan** — searched for TBD/TODO/"add appropriate"/"similar to": none in the plan. Every code step has runnable code.

**Type consistency** — `SessionShape` in Task 7 matches `useSession` consumers in Task 9 (LoginForm) and Task 10 step 1 (placeholder page). `ApiError` is defined in Task 4 step 2 and used in Task 7 (hooks.ts) and Task 9 step 2 (LoginForm). `setJwt`/`clearJwt`/`getJwt` are defined in Task 4 step 2 and used in Task 7 step 3.

Plan is complete.
