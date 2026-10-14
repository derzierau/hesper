import styles from "./OneKey.module.css";
import { StateMark } from "@/components/ui/StateMark";
import { OneKeyTile } from "./OneKeyTile";

const queue = [
  { kind: "needsYou", label: "Approval" },
  { kind: "question", label: "Question" },
  { kind: "error", label: "Error" },
] as const;

/** The ⌘J moment on its own: text left, one tile the visitor can answer right. */
export function OneKey() {
  return (
    <section className="section" id="onekey">
      <div className={`wrap ${styles.grid}`}>
        <div>
          <h2>One queue, one key.</h2>
          <p className="lede">
            ⌘J jumps to the next agent that needs you: approvals first, then questions, then errors.
            Allow, always-allow or deny right on the tile. Tiles never move to get your attention;
            the one that needs you lights up.
          </p>
          <ul className={styles.legend} aria-label="Queue order">
            {queue.map((q) => (
              <li key={q.kind} className={styles.legendItem}>
                <StateMark kind={q.kind} />
                {q.label}
              </li>
            ))}
            <li className={styles.legendNote}>That is the order ⌘J uses.</li>
          </ul>
        </div>
        <OneKeyTile />
      </div>
    </section>
  );
}
