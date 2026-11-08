import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

// The three page widths. Every full page picks one:
//   narrow  — forms and single-column settings (max 768px)
//   default — lists, tables, card grids (max 1152px)
//   wide    — dashboards and data-dense views (max 1536px)
export type PageWidth = "narrow" | "default" | "wide";

const widths: Record<PageWidth, string> = {
  narrow: "max-w-3xl",
  default: "max-w-6xl",
  wide: "max-w-screen-2xl",
};

export function PageContainer({
  width = "default",
  className,
  children,
}: {
  width?: PageWidth;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      data-slot="page-container"
      className={cn("mx-auto w-full px-4 pt-6 pb-12 sm:px-6 sm:pt-8 lg:px-8", widths[width], className)}
    >
      {children}
    </div>
  );
}
