import styles from "./StateMark.module.css";

/** Agent states as the app draws them: 7 px squares, never dots. */
export type MarkKind = "starting" | "working" | "needsYou" | "question" | "done" | "idle" | "error" | "exited";

export function StateMark({ kind, className = "" }: { kind: MarkKind; className?: string }) {
  return <span className={`${styles.mark} ${styles[kind]} ${className}`} aria-hidden="true" />;
}
