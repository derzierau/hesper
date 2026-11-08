import styles from "./PickUp.module.css";
import { Kbd } from "../ui/Kbd";
import { site } from "../../lib/content";
import { HistoryStill, ReviewStill } from "./PickUpStills";

export function PickUp() {
  return (
    <section className="section" id="pickup">
      <div className="wrap">
        <h2>Your work, from any Mac.</h2>

        <div className={styles.grid}>
          <article className={styles.card}>
            <HistoryStill />
            <div className={styles.body}>
              <h3 className={styles.h3}>History <Kbd>⌘Y</Kbd></h3>
              <p className={styles.copy}>
                One history across all your Macs. Search every Claude and Codex session you ever ran.
                Resume it where it ran, continue it on another Mac, fork it, or hand it from Claude to Codex.
              </p>
            </div>
          </article>

          <article className={styles.card}>
            <ReviewStill />
            <div className={styles.body}>
              <h3 className={styles.h3}>Review <Kbd>⌘R</Kbd></h3>
              <p className={styles.copy}>
                Review what came back. Finished work from every Mac in one inbox, as a native diff in reading order.
                See the commands the agent ran and which turn wrote each line. Accept, reject or send it back with notes.
              </p>
              <p className={styles.note}>New in 0.2.0.</p>
            </div>
          </article>
        </div>
      </div>
    </section>
  );
}
