"use client";

import { useTheme } from "next-themes";
import { Moon, Sun, Monitor } from "lucide-react";
import { useEffect, useState } from "react";

export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  if (!mounted) return <button className="h-8 w-8" aria-hidden />;
  const cycle = () =>
    setTheme(theme === "light" ? "dark" : theme === "dark" ? "system" : "light");
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  const current = theme === "light" ? "light" : theme === "dark" ? "dark" : "system";
  const next = theme === "light" ? "dark" : theme === "dark" ? "system" : "light";
  return (
    <button
      type="button"
      onClick={cycle}
      aria-label={`Theme: ${current}. Switch to ${next}`}
      title={`Theme: ${current}`}
      className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
    >
      <Icon className="h-4 w-4" />
    </button>
  );
}
