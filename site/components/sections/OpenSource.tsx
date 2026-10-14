import { StateMark } from "@/components/ui/StateMark";
import { site } from "@/lib/content";
import styles from "./OpenSource.module.css";

export function OpenSource() {
  return (
    <section className="section" id="opensource">
      <div className="wrap">
        <h2>MIT, in the open.</h2>

        <div className={styles.card}>
          <a className={styles.title} href={site.repo}>{site.repo.replace("https://", "")}</a>
          <p className={styles.stack}>Swift 6 and Go 1.25, MIT licensed.</p>
          <ul className={styles.facts}>
            <li><StateMark kind="done" />{site.tests.go} Go tests</li>
            <li><StateMark kind="done" />{site.tests.swift} Swift tests</li>
            <li><StateMark kind="done" /><a href={site.protocol}>a written wire protocol</a></li>
            <li><StateMark kind="done" /><a href={site.architecture}>an architecture doc</a></li>
          </ul>
          <div className={styles.actions}>
            <a className={styles.ghost} href={site.repo}>Clone source</a>
            <a className={styles.ghost} href={site.contributing}>Contribute</a>
          </div>
        </div>

        <p className={styles.note}>
          Hesper is young and used daily by its author. Every number on this page has a measurement behind it,
          and every claim is in the README.
        </p>
      </div>
    </section>
  );
}
