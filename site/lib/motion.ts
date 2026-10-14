"use client";
import { useEffect, useRef, useState } from "react";

/** True when the visitor asked the OS for reduced motion. Updates live. */
export function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(mq.matches);
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);
  return reduced;
}

/**
 * Fires once when the element is in view. The page's choreography starts from
 * this signal, so nothing animates off-screen and nothing replays on scroll.
 * `threshold` is the fraction of the element that must be visible.
 */
export function useInView<T extends HTMLElement>(threshold = 0.4): [React.RefObject<T | null>, boolean] {
  const ref = useRef<T>(null);
  const [inView, setInView] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setInView(true);
          io.disconnect();
        }
      },
      { threshold },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [threshold]);
  return [ref, inView];
}

/** Pressing a key while `active`. `combo` is like "meta+j", "meta+z" or "enter". */
export function useKey(combo: string, active: boolean, handler: () => void) {
  useEffect(() => {
    if (!active) return;
    const parts = combo.toLowerCase().split("+");
    const key = parts[parts.length - 1];
    const meta = parts.includes("meta");
    const shift = parts.includes("shift");
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey !== meta || e.shiftKey !== shift) return;
      if (e.key.toLowerCase() !== key) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      e.preventDefault();
      handler();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [combo, active, handler]);
}

/** Steps a counter from 0 to `steps` with `delay` ms between steps, once `start` is true. Returns the current step. */
export function useSequence(steps: number, delay: number, start: boolean, instant = false): number {
  const [step, setStep] = useState(0);
  useEffect(() => {
    if (!start) return;
    if (instant) { setStep(steps); return; }
    let i = 0;
    const id = setInterval(() => {
      i += 1;
      setStep(i);
      if (i >= steps) clearInterval(id);
    }, delay);
    return () => clearInterval(id);
  }, [steps, delay, start, instant]);
  return step;
}
