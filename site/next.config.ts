import type { NextConfig } from "next";

// Static export: the site is plain files, served by GitHub Pages or any
// web server. BASE_PATH is set in CI when the site lives under /hesper/.
const nextConfig: NextConfig = {
  output: "export",
  trailingSlash: true,
  basePath: process.env.BASE_PATH ?? "",
  images: { unoptimized: true },
};

export default nextConfig;
