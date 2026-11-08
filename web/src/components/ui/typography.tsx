import * as React from "react"

import { cn } from "@/lib/utils"

// Type roles. Sans for anything a person reads (labels, hints, prose);
// mono ONLY for literal values a person might copy — hashes, hosts,
// image refs, env keys, commands. Size floor is 12px (text-xs).

/** Short secondary explanation under a field or heading. */
function Hint({ className, ...props }: React.ComponentProps<"p">) {
  return <p data-slot="hint" className={cn("text-hint", className)} {...props} />
}

/** A literal machine value. `muted` drops it to the tertiary colour. */
function Mono({
  className,
  muted = false,
  ...props
}: React.ComponentProps<"code"> & { muted?: boolean }) {
  return (
    <code
      data-slot="mono"
      className={cn(
        "font-mono text-[0.92em] break-all",
        muted ? "text-[var(--text-tertiary)]" : "text-[var(--text-primary)]",
        className
      )}
      {...props}
    />
  )
}

/** Heading for a group of fields/rows inside a page or panel. Sentence
 *  case, not tracked-out caps. */
function SectionTitle({
  className,
  as: Tag = "h2",
  ...props
}: React.ComponentProps<"h2"> & { as?: "h2" | "h3" | "h4" }) {
  return (
    <Tag
      data-slot="section-title"
      className={cn("text-sm font-semibold tracking-tight text-[var(--text-primary)]", className)}
      {...props}
    />
  )
}

export { Hint, Mono, SectionTitle }
