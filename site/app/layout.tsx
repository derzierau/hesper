import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geist = Geist({ subsets: ["latin"], weight: ["400", "500", "600"], variable: "--font-geist", display: "swap" });
const geistMono = Geist_Mono({ subsets: ["latin"], weight: ["400", "500"], variable: "--font-geist-mono", display: "swap" });

const siteUrl = process.env.SITE_URL ?? "https://derzierau.github.io/hesper";

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),
  title: "Hesper",
  description:
    "Your Macs. Your agents. One window. Hesper runs Claude Code and Codex on every Mac you own and shows them as live terminal tiles on one wall. Free, open source, nothing leaves your machines.",
  openGraph: {
    title: "Hesper",
    description: "Your Macs. Your agents. One window.",
    type: "website",
    images: [{ url: "/og.png", width: 1200, height: 630, alt: "Hesper: your Macs, your agents, one window." }],
  },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${geist.variable} ${geistMono.variable}`}>
      <body>{children}</body>
    </html>
  );
}
