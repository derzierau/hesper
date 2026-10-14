import styles from "./Pillars.module.css";
import { Kbd } from "@/components/ui/Kbd";

/** Three columns under one hairline, no heading: the columns are the section. */
export function Pillars() {
  return (
    <section className="section" id="pillars">
      <div className="wrap">
        <ul className={styles.columns}>
          <li className={styles.column}>
            <h3 className={styles.title}>One wall, one queue.</h3>
            <p className={styles.body}>
              Every agent is a live terminal tile. <Kbd>⌘J</Kbd> jumps to the next one that needs you:
              approvals first, then questions, then errors. <Kbd>⏎</Kbd> <Kbd>A</Kbd> <Kbd>N</Kbd> answer
              it right on the tile.
            </p>
          </li>
          <li className={styles.column}>
            <h3 className={styles.title}>Every Mac is compute.</h3>
            <p className={styles.body}>
              Start an agent on any Mac from any Mac. Move or fork a running agent, with its uncommitted
              changes and its conversation. One <Kbd>⌘Z</Kbd> moves it back.
            </p>
          </li>
          <li className={styles.column}>
            <h3 className={styles.title}>Yours, end to end.</h3>
            <p className={styles.body}>
              Native AppKit on libghostty, no web view. MIT. A single Mac needs no relay. Several Macs
              talk through a relay you host, encrypted so the relay cannot read or forge anything.
            </p>
          </li>
        </ul>
      </div>
    </section>
  );
}
