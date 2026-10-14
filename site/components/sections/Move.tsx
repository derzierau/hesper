import styles from "./Move.module.css";
import { Kbd } from "@/components/ui/Kbd";
import { MoveDemo } from "./MoveDemo";

/** The differentiator: a move the visitor can drive, then three facts. */
export function Move() {
  return (
    <section className="section" id="move">
      <div className="wrap">
        <h2>Move work to where there is room.</h2>
        <p className="lede">
          Hand off a running agent. Continue on another Mac checkpoints the worktree (staged,
          unstaged and untracked), sends only the changes, sealed, and resumes the same Claude or
          Codex conversation there. One ⌘Z moves it back. Fork keeps the original and starts a
          twin. If the other Mac lacks the folder, Hesper brings it along.
        </p>

        <MoveDemo />

        <dl className={styles.facts}>
          <div className={styles.fact}>
            <dt>Undo</dt>
            <dd>one <Kbd>⌘Z</Kbd></dd>
          </div>
          <div className={styles.fact}>
            <dt>Fork</dt>
            <dd>keeps the original, starts a twin</dd>
          </div>
          <div className={styles.fact}>
            <dt>Resumable transfers</dt>
            <dd>up to <span className={styles.num}>5 GB</span> by default</dd>
          </div>
        </dl>
      </div>
    </section>
  );
}
