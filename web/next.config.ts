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
