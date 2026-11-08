"use client";
import { useEffect, useState } from "react";
import { Wordmark } from "@/components/ui/Wordmark";
import { site } from "@/lib/content";
import styles from "./Nav.module.css";

const links = [
  { label: "Wall", href: "#onekey" },
  { label: "Speed", href: "#speed" },
  { label: "Security", href: "#security" },
  { label: "Docs", href: `${site.repo}#readme` },
] as const;

/**
 * Sticky 48 px bar. Solid surface, one line below, no blur. When the wall
 * says an agent needs you, the wordmark's square pulses twice and rests.
 */
export function Nav() {
  const [pulse, setPulse] = useState(false);

  useEffect(() => {
    const on = () => setPulse(true);
    window.addEventListener("hesper:needs-you", on);
    return () => window.removeEventListener("hesper:needs-you", on);
  }, []);

  return (
    <header className={styles.bar}>
      <nav className={`wrap ${styles.inner}`} aria-label="Site">
        <a
          href="#top"
          className={`${styles.home} ${pulse ? styles.pulse : ""}`}
          aria-label="Hesper, top of page"
          onAnimationEnd={() => setPulse(false)}
        >
          <Wordmark size={17} />
        </a>
        <ul className={styles.links}>
          {links.map((l) => (
            <li key={l.href} className={styles.hideNarrow}>
              <a href={l.href} className={styles.link}>{l.label}</a>
            </li>
          ))}
          <li>
            <a href={site.repo} className={styles.link}>GitHub</a>
          </li>
          <li>
            <a href="#install" className={styles.install}>Install</a>
          </li>
        </ul>
      </nav>
    </header>
  );
}
