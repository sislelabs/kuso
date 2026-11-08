import type { Metadata, Viewport } from "next";
import { Instrument_Sans, Geist_Mono } from "next/font/google";
import { ThemeProvider } from "next-themes";
import { QueryProvider } from "@/lib/query-client";
import { ThemedToaster } from "@/components/shared/ThemedToaster";
import { ReducedMotion } from "@/lib/reduced-motion";
import "./globals.css";

// One UI family for prose, labels and headings; Geist Mono only for
// literal values (hashes, hosts, keys, logs).
const uiSans = Instrument_Sans({
  subsets: ["latin"],
  variable: "--font-ui",
  display: "swap",
});
const geistMono = Geist_Mono({
  subsets: ["latin"],
  variable: "--font-geist-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: "kuso",
  description: "Self-hosted, agent-native PaaS for indie developers.",
  icons: {
    // Versioned URL so cache-busting works on favicon updates. Bump
    // v= when the SVG changes; otherwise tabs pinned for weeks keep
    // showing the stale icon.
    icon: [
      { url: "/favicon.svg?v=4", type: "image/svg+xml" },
      { url: "/favicon.ico?v=4", sizes: "any" },
    ],
  },
};

// Declared explicitly rather than relying on Next's injected default.
// viewportFit=cover lets full-screen overlays + the log panel reach
// under notches; maximumScale is intentionally left unset so users can
// still pinch-zoom (an accessibility floor we don't want to break).
export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#FBFBF7" },
    { media: "(prefers-color-scheme: dark)", color: "#0A0C15" },
  ],
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html
      lang="en"
      suppressHydrationWarning
      className={`${uiSans.variable} ${geistMono.variable}`}
    >
      <body className="font-sans antialiased">
        <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
          <ReducedMotion>
            <QueryProvider>
              {children}
              <ThemedToaster />
            </QueryProvider>
          </ReducedMotion>
        </ThemeProvider>
      </body>
    </html>
  );
}
