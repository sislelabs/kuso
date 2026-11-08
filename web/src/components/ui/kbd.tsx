import * as React from "react"

import { cn } from "@/lib/utils"

// Keyboard key cap. `Kbd` is one key; `KbdCombo` renders a chord
// (["⌘","K"]) or a sequence (["G","P"], with `sequence`) from one list.
function Kbd({ className, ...props }: React.ComponentProps<"kbd">) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded border border-[var(--border-default)] border-b-2 bg-[var(--bg-secondary)] px-1 font-sans text-2xs font-medium text-[var(--text-secondary)]",
        className
      )}
      {...props}
    />
  )
}

function KbdCombo({
  keys,
  sequence = false,
  className,
}: {
  keys: readonly string[]
  /** Keys pressed one after another (shows "then") rather than together. */
  sequence?: boolean
  className?: string
}) {
  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      {keys.map((k, i) => (
        <React.Fragment key={`${k}-${i}`}>
          {sequence && i > 0 && (
            <span className="text-2xs text-[var(--text-tertiary)]">then</span>
          )}
          <Kbd>{k}</Kbd>
        </React.Fragment>
      ))}
    </span>
  )
}

export { Kbd, KbdCombo }
