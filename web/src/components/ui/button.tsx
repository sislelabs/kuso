import { Button as ButtonPrimitive } from "@base-ui/react/button"
import { cva, type VariantProps } from "class-variance-authority"
import { Loader2 } from "lucide-react"

import { cn } from "@/lib/utils"

// Small sizes keep their visual size on touch screens but get an
// invisible hit area (the ::after) so taps land on a ≥40px target.
const touchTarget =
  "pointer-coarse:after:absolute pointer-coarse:after:top-1/2 pointer-coarse:after:left-1/2 pointer-coarse:after:size-full pointer-coarse:after:min-h-10 pointer-coarse:after:min-w-10 pointer-coarse:after:-translate-x-1/2 pointer-coarse:after:-translate-y-1/2 pointer-coarse:after:content-['']"

// One primary style (sage) — the "do the thing" action. Everything else
// is secondary (bordered surface), ghost, destructive or link.
// `default`, `accent`, `neutral` and `outline` are kept as aliases so
// existing call sites keep compiling; prefer the canonical names.
const primary =
  "border-[var(--btn-primary-border)] bg-[var(--btn-primary-bg)] text-[var(--btn-primary-fg)] shadow-[var(--shadow-sm)] hover:bg-[var(--btn-primary-bg-hover)]"
const secondary =
  "border-[var(--btn-secondary-border)] bg-[var(--btn-secondary-bg)] text-[var(--btn-secondary-fg)] shadow-[var(--shadow-sm)] hover:border-[var(--border-strong)] hover:bg-[var(--btn-secondary-bg-hover)] aria-expanded:bg-[var(--btn-secondary-bg-hover)]"

const buttonVariants = cva(
  touchTarget + " " + "group/button relative inline-flex shrink-0 items-center justify-center rounded-md border border-transparent bg-clip-padding text-sm font-medium whitespace-nowrap transition-[background-color,border-color,color,box-shadow] duration-150 outline-none select-none active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 aria-invalid:border-destructive [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        primary,
        default: primary,
        accent: primary,
        secondary,
        outline: secondary,
        neutral: secondary,
        ghost:
          "text-[var(--text-secondary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] aria-expanded:bg-[var(--bg-tertiary)] aria-expanded:text-[var(--text-primary)]",
        destructive:
          "border-transparent bg-[var(--danger)] text-[var(--bg-primary)] shadow-[var(--shadow-sm)] hover:brightness-110",
        "destructive-subtle":
          "border-[var(--danger)]/30 bg-[var(--danger-subtle)] text-[var(--danger)] hover:border-[var(--danger)]/60",
        link: "h-auto! border-0 px-0! text-[var(--link)] underline-offset-4 hover:underline",
      },
      size: {
        default:
          "h-9 gap-1.5 px-4 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
        xs: "h-6 gap-1 px-2 text-xs has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-8 gap-1.5 px-3 text-[13px] has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2 [&_svg:not([class*='size-'])]:size-3.5",
        lg: "h-10 gap-2 px-5 has-data-[icon=inline-end]:pr-4 has-data-[icon=inline-start]:pl-4",
        icon: "size-9",
        "icon-xs": "size-6 [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-8 [&_svg:not([class*='size-'])]:size-3.5",
        "icon-lg": "size-10",
      },
    },
    defaultVariants: {
      variant: "primary",
      size: "default",
    },
  }
)

type ButtonProps = ButtonPrimitive.Props &
  VariantProps<typeof buttonVariants> & {
    /** Shows a spinner, sets aria-busy and blocks clicks while true. */
    loading?: boolean
  }

function Button({
  className,
  variant = "primary",
  size = "default",
  loading = false,
  disabled,
  children,
  ...props
}: ButtonProps) {
  return (
    <ButtonPrimitive
      data-slot="button"
      data-variant={variant}
      aria-busy={loading || undefined}
      disabled={disabled || loading}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    >
      {loading && <Loader2 aria-hidden className="animate-spin" />}
      {children}
    </ButtonPrimitive>
  )
}

export { Button, buttonVariants }
export type { ButtonProps }
