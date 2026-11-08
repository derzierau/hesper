import styles from "./Pill.module.css";
import { StateMark, type MarkKind } from "./StateMark";
import { Kbd } from "./Kbd";

/** Radius control, height 22. Status pills are tinted with their mark's tone at 14%. */
export function Pill({ children, mark, kbd, count }: { children?: React.ReactNode; mark?: MarkKind; kbd?: string; count?: number }) {
  const tone = mark ? styles[mark] : "";
  return (
    <span className={`${styles.pill} ${tone}`}>
      {mark && <StateMark kind={mark} />}
      {children}
      {count !== undefined && <span className={styles.count}>{count}</span>}
      {kbd && <Kbd>{kbd}</Kbd>}
    </span>
  );
}
