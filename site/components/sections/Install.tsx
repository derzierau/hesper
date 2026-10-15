import { CopyCommand } from "@/components/ui/CopyCommand";
import { site } from "@/lib/content";
import styles from "./Install.module.css";

export function Install() {
  return (
    <section className="section" id="install">
      <div className="wrap">
        <h2>Four lines with Homebrew.</h2>

        <CopyCommand lines={site.brew} className={styles.command} />

        <ul className={styles.requirements}>
          {site.requirements.map((item) => <li key={item}>{item}</li>)}
        </ul>
        <p className={styles.hooks}>Then run <code>/hooks</code> once in Codex.</p>

        <p className={styles.multi}>
          A single Mac needs nothing more. Several Macs talk through a relay you host; it needs a public DNS name
          and a GitHub OAuth app. The <a href={site.deployment}>deployment guide</a> walks through it.
        </p>

        <p className={styles.quiet}>
          Prefer a zip? Signed and notarized builds for Apple Silicon and Intel are on
          the <a href={site.releases}>releases page</a>.
        </p>
        <p className={styles.version}>Current release {site.version}</p>
      </div>
    </section>
  );
}
