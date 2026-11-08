"use client";
import { useEffect, useState } from "react";
import styles from "./Speed.module.css";
import { site } from "@/lib/content";
import { useInView, useReducedMotion } from "@/lib/motion";

const streaming = "https://github.com/derzierau/hesper/blob/main/relay/docs/terminal-streaming.md";

/* Mirrors --m-travel and --ease in globals.css. */
const travel = 600;
function bezier(x1: number, y1: number, x2: number, y2: number) {
  const at = (a: number, b: number, s: number) => 3 * a * s * (1 - s) * (1 - s) + 3 * b * s * s * (1 - s) + s * s * s;
  return (x: number) => {
    let lo = 0;
    let hi = 1;
    for (let i = 0; i < 24; i++) {
      const mid = (lo + hi) / 2;
      if (at(x1, x2, mid) < x) lo = mid; else hi = mid;
    }
    return at(y1, y2, (lo + hi) / 2);
  };
}
const ease = bezier(0.2, 0, 0, 1);

/**
 * The leading number of a result counts up from 0 once `start` is true; the
 * rest of the string ("/ 45 ms", ", 0 dropped") stays. The server renders the
 * final value, and so does `instant` (reduced motion).
 */
function CountUp({ value, start, instant }: { value: string; start: boolean; instant: boolean }) {
  const m = /^(\d+(?:\.\d+)?)(.*)$/.exec(value);
  const target = m ? Number(m[1]) : 0;
  const decimals = m?.[1].split(".")[1]?.length ?? 0;
  const [progress, setProgress] = useState(1);

  useEffect(() => {
    if (instant) { setProgress(1); return; }
    if (!start) { setProgress(0); return; }
    let raf = 0;
    let t0 = 0;
    const tick = (now: number) => {
      if (!t0) t0 = now;
      const p = Math.min(1, (now - t0) / travel);
      setProgress(ease(p));
      if (p < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [start, instant]);

  if (!m) return <>{value}</>;
  return <>{(target * progress).toFixed(decimals)}{m[2]}</>;
}

/** The measurement table. Rows come from lib/content.ts; nothing is retyped here. */
export function Speed() {
  const reduced = useReducedMotion();
  const [ref, inView] = useInView<HTMLElement>();
  return (
    <section className="section" id="speed">
      <div className="wrap">
        <h2>Every layer trimmed for latency.</h2>
        <p className="lede">
          A native AppKit app on libghostty, a small Go daemon per Mac, and a budget for every hop.
        </p>

        <figure ref={ref} className={styles.figure}>
          <div className={styles.scroll}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th scope="col">Measurement</th>
                  <th scope="col">Condition</th>
                  <th scope="col" className={styles.result}>Result</th>
                </tr>
              </thead>
              <tbody>
                {site.speed.map((row) => (
                  <tr key={row.what}>
                    <td className={styles.what}>{row.what}</td>
                    <td className={styles.condition}>{row.condition}</td>
                    <td className={styles.result}>
                      <CountUp value={row.result} start={inView} instant={reduced} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <figcaption className={`caption ${styles.note}`}>
            Measured on Apple Silicon, p50 unless noted. Method and raw numbers are in the{" "}
            <a href={streaming}>terminal streaming doc</a>.
          </figcaption>
        </figure>
      </div>
    </section>
  );
}
