import { StateMark } from "@/components/ui/StateMark";
import { site } from "@/lib/content";
import styles from "./Security.module.css";

/** What the relay sees and what it never sees, from relay/docs/architecture.md. */
const sees = [
  "who talks to whom, when and how much",
  "agent ids, states and machine stats",
  "device public keys and names",
];
const neverSees = [
  "names, tasks and paths",
  "terminal output",
  "your GitHub token after sign-in",
];

export function Security() {
  return (
    <section className="section" id="security">
      <div className="wrap">
        <h2>The relay cannot type, answer, start, stop or move anything.</h2>
        <p className="lede">
          Every request is signed by a key in your Mac&rsquo;s Secure Enclave and checked by the receiving Mac.
          Terminal traffic is encrypted between your Macs with pinned keys. Macs on the same network skip the relay.
        </p>

        <div className={styles.table}>
          <div className={styles.col}>
            <h3 className={styles.head}>What the relay sees</h3>
            <ul className={styles.rows}>
              {sees.map((row) => (
                <li key={row} className={styles.row}><StateMark kind="idle" />{row}</li>
              ))}
            </ul>
          </div>
          <div className={styles.col}>
            <h3 className={styles.head}>What it never sees</h3>
            <ul className={styles.rows}>
              {neverSees.map((row) => (
                <li key={row} className={styles.row}><StateMark kind="done" />{row}</li>
              ))}
            </ul>
          </div>
        </div>

        <p className={styles.facts}>
          Your Macs talk <a href={site.protocol}><code>Noise_IK_25519_ChaChaPoly_BLAKE2s</code></a> to each other,
          and controllers rekey every hour for <a href={site.architecture}>forward secrecy</a>.
          A <a href={`${site.repo}#more-than-one-mac`}>single Mac</a> needs no relay at all.
        </p>
        <p className={styles.more}><a href={site.architecture}>Read the architecture</a></p>
      </div>
    </section>
  );
}
