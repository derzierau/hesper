import styles from "./Wordmark.module.css";

/** "hesper." The full stop is the signal square from the app icon. */
export function Wordmark({ size = 17 }: { size?: number }) {
  return (
    <span className={styles.mark} style={{ fontSize: size }} aria-label="hesper">
      hesper<i className={styles.dot} aria-hidden="true" />
    </span>
  );
}
