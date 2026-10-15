import { CopyCommand } from "@/components/ui/CopyCommand";
import { Wall } from "@/components/wall/Wall";
import { site } from "@/lib/content";
import styles from "./Hero.module.css";

/** Copy on the left (5/12), the live wall on the right (7/12). Sized to content, not the viewport. */
export function Hero() {
  return (
    <section className={styles.hero} id="top">
      <div className={`wrap ${styles.grid}`}>
        <div className={styles.copy}>
          <h1 className={styles.title}>
            <span className={styles.line}>Your Macs.</span>
            <span className={styles.line}>Your agents.</span>
            <span className={styles.line}>One window.</span>
          </h1>
          <p className={styles.sub}>
            Hesper runs Claude Code and Codex on your laptop, the Mac mini under the desk and the Studio in the office.
            Every agent is a live terminal tile on one wall. The ones that need you are one key away.
          </p>
          <p className={`caption ${styles.fine}`}>
            Free and MIT licensed. macOS 14 or newer, Apple Silicon and Intel, signed and notarized. Nothing leaves your machines.
          </p>
          <div className={styles.ctas}>
            <CopyCommand lines={[site.brew[1]]} className={styles.cmd} />
            <a href={site.repo} className={styles.ghost}>View source on GitHub</a>
          </div>
        </div>
        <div className={styles.wall}>
          <Wall />
        </div>
      </div>
    </section>
  );
}
