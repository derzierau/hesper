import styles from "./Tile.module.css";
import { StateMark, type MarkKind } from "./StateMark";
import { Kbd } from "./Kbd";

/**
 * One agent tile as the wall draws it: 24 px header (mark, name, meta), a
 * terminal body on the TokyoNight background, and an optional footer band
 * for the prompt that needs you. Keep the anatomy; vary the content.
 */
export function Tile({
  name, machine, age, kind, children, footer, className = "",
}: {
  name: string; machine: string; age?: string; kind: MarkKind;
  children: React.ReactNode; footer?: string; className?: string;
}) {
  return (
    <div className={`${styles.tile} ${className}`} data-state={kind}>
      <header className={styles.header}>
        <StateMark kind={kind} />
        <span className={styles.name}>{name}</span>
        <span className={styles.meta}>{machine}{age ? ` · ${age}` : ""}</span>
      </header>
      <pre className={styles.term}>{children}</pre>
      {footer && (
        <footer className={styles.footer}>
          <StateMark kind="needsYou" />
          <span className={styles.prompt}>{footer}</span>
          <Kbd>⏎</Kbd><Kbd>A</Kbd><Kbd>N</Kbd>
        </footer>
      )}
    </div>
  );
}
