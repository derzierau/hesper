import styles from "./Kbd.module.css";

/** Geist Mono 10.5 medium, 1 px line border, radius kbd, dim text. */
export function Kbd({ children }: { children: React.ReactNode }) {
  return <kbd className={styles.kbd}>{children}</kbd>;
}
