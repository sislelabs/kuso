"use client";

import { useMemo, useRef, useState } from "react";
import { Eye, EyeOff, FileUp, Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { looksSecret, parseDotenv, type DotenvResult } from "@/features/services/dotenv";

// Files bigger than this aren't a .env.
const MAX_ENV_FILE_BYTES = 256 * 1024;

export function useDotenv(text: string): DotenvResult {
  return useMemo(() => parseDotenv(text), [text]);
}

// EnvPastePanel takes a pasted or uploaded .env before the first build
// and previews what will be set. The textarea is marked data-no-draft so
// the session-expiry draft snapshot never copies secrets to storage.
export function EnvPastePanel({
  text,
  onChange,
  parsed,
  id = "svc-env",
}: {
  text: string;
  onChange: (text: string) => void;
  parsed: DotenvResult;
  id?: string;
}) {
  const fileRef = useRef<HTMLInputElement>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const [reveal, setReveal] = useState(false);
  const { vars, errors } = parsed;
  const secretCount = vars.filter((v) => looksSecret(v.name, v.value)).length;

  const onFile = async (f: File | undefined) => {
    setFileError(null);
    if (!f) return;
    if (f.size > MAX_ENV_FILE_BYTES) {
      setFileError(`${f.name} is ${Math.round(f.size / 1024)} KB; a .env file should be well under 256 KB.`);
      return;
    }
    try {
      const content = await f.text();
      onChange(text.trim() ? `${text.replace(/\n+$/, "")}\n${content}` : content);
    } catch {
      setFileError(`Couldn't read ${f.name}.`);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" variant="outline" onClick={() => fileRef.current?.click()}>
          <FileUp className="h-3.5 w-3.5" />
          Upload .env file
        </Button>
        <input
          ref={fileRef}
          type="file"
          accept=".env,.txt,text/plain"
          className="hidden"
          aria-hidden
          tabIndex={-1}
          data-no-draft
          onChange={(e) => {
            void onFile(e.target.files?.[0]);
            e.target.value = "";
          }}
        />
        <span className="text-xs text-[var(--text-secondary)]">or paste its contents below</span>
      </div>
      <textarea
        id={id}
        value={text}
        onChange={(e) => onChange(e.target.value)}
        data-no-draft
        spellCheck={false}
        autoComplete="off"
        rows={Math.min(12, Math.max(4, text.split("\n").length + 1))}
        placeholder={"# e.g.\nDATABASE_URL=postgres://…\nSTRIPE_SECRET_KEY=sk_live_…\nexport LOG_LEVEL=info"}
        aria-invalid={errors.length > 0 ? true : undefined}
        aria-describedby={`${id}-summary`}
        className="block w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2 font-mono text-[12px] leading-relaxed placeholder:text-[var(--text-tertiary)] focus-visible:border-[var(--accent)] focus-visible:outline-none aria-invalid:border-[var(--error)]"
      />
      {fileError && (
        <p role="alert" className="text-xs text-[var(--error)]">
          {fileError}
        </p>
      )}
      <div id={`${id}-summary`} className="space-y-2">
        {errors.length > 0 && (
          <ul role="alert" className="space-y-0.5 text-xs text-[var(--error)]">
            {errors.slice(0, 8).map((e) => (
              <li key={`${e.line}-${e.message}`}>
                Line {e.line}: {e.message}
              </li>
            ))}
            {errors.length > 8 && <li>…and {errors.length - 8} more</li>}
          </ul>
        )}
        {vars.length > 0 && (
          <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)]">
            <div className="flex items-center justify-between gap-2 border-b border-[var(--border-subtle)] px-3 py-1.5 text-xs text-[var(--text-secondary)]">
              <span>
                {vars.length} {vars.length === 1 ? "variable" : "variables"}
                {secretCount > 0 && ` · ${secretCount} look secret`}
              </span>
              <button
                type="button"
                onClick={() => setReveal((r) => !r)}
                className="flex items-center gap-1 hover:text-[var(--text-primary)]"
                aria-pressed={reveal}
              >
                {reveal ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                {reveal ? "Hide values" : "Show values"}
              </button>
            </div>
            <ul className="max-h-48 divide-y divide-[var(--border-subtle)] overflow-auto">
              {vars.map((v) => {
                const secret = looksSecret(v.name, v.value);
                return (
                  <li key={v.name} className="flex min-w-0 items-center gap-3 px-3 py-1.5 font-mono text-[12px]">
                    <span className="flex shrink-0 items-center gap-1 text-[var(--text-primary)]">
                      {secret && <Lock className="h-3 w-3 text-[var(--text-tertiary)]" aria-label="secret" />}
                      {v.name}
                    </span>
                    <span className="min-w-0 truncate text-[var(--text-secondary)]">
                      {v.value === "" ? (
                        <span className="font-sans italic text-[var(--text-tertiary)]">empty</span>
                      ) : secret && !reveal ? (
                        "•".repeat(Math.min(12, Math.max(6, v.value.length)))
                      ) : (
                        v.value.replace(/\n/g, "⏎")
                      )}
                    </span>
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </div>
      <p className="text-xs leading-relaxed text-[var(--text-secondary)]">
        Saved before the first deploy. Secrets go to the service&apos;s Kubernetes Secret, not its
        spec. Use <span className="whitespace-nowrap font-mono">{"${{ db.DATABASE_URL }}"}</span> to reference an addon.
      </p>
    </div>
  );
}
