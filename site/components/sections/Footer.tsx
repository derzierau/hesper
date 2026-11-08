import { Wordmark } from "@/components/ui/Wordmark";
import { site } from "@/lib/content";
import styles from "./Footer.module.css";

const links = [
  { label: "GitHub", href: site.repo },
  { label: "Releases", href: site.releases },
  { label: "Security", href: site.security },
  { label: "License", href: `${site.repo}/blob/main/LICENSE` },
] as const;

export function Footer() {
  return (
    <footer className={styles.footer}>
      <div className="wrap">
        <div className={styles.row1}>
          <Wordmark size={17} />
          <p className={styles.tagline}>
            Named after Hesperos, the evening star. Your agents run quietly across your Macs; Hesper lights up the one that needs you.
          </p>
        </div>
        <ul className={styles.links}>
          {links.map((l) => (
            <li key={l.label}><a href={l.href} className={styles.link}>{l.label}</a></li>
          ))}
        </ul>
        <p className={`caption ${styles.note}`}>Built on libghostty. An independent project, not affiliated with Ghostty.</p>
        <p className={`caption ${styles.note}`}>MIT licensed. Copyright 2026 Ole Zierau.</p>
      </div>
    </footer>
  );
}
