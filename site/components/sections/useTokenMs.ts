"use client";
import { useEffect, useState } from "react";

/** Reads a motion token from :root as milliseconds, so a timer runs on the same value the CSS does. The tokens are declared in ms. */
export function useTokenMs(name: string, fallback: number): number {
  const [ms, setMs] = useState(fallback);
  useEffect(() => {
    const v = parseFloat(getComputedStyle(document.documentElement).getPropertyValue(name));
    if (!Number.isNaN(v)) setMs(v);
  }, [name]);
  return ms;
}
