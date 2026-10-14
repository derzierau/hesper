"use client";
import styles from "./PickUp.module.css";
import { StateMark, type MarkKind } from "@/components/ui/StateMark";
import { useInView, useReducedMotion, useSequence } from "@/lib/motion";
import { useTokenMs } from "./useTokenMs";

/**
 * The two stills of PickUp, each with its one moment: History types its
 * query and lists the results; Review lets the diff arrive and marks the
 * review done. Both start when in view and render the finished state under
 * reduced motion. Fixture text only.
 */

/** One row as History and Review draw it: mark, title, subtitle, meta. */
function Row({ kind, title, sub, meta, selected = false, shown = true }: {
  kind: MarkKind; title: string; sub: string; meta: string; selected?: boolean; shown?: boolean;
}) {
  return (
    <div className={`${styles.row} ${selected ? styles.selected : ""} ${shown ? "" : styles.pending}`}>
      <StateMark kind={kind} />
      <span className={styles.rowText}>
        <span className={styles.title}>{title}</span>
        <span className={styles.sub}>{sub}</span>
      </span>
      <span className={styles.meta}>{meta}</span>
    </div>
  );
}

const query = "rate limit";
const sessions = [
  { kind: "done", title: "Add rate limits to /login", sub: "laptop · orbit", meta: "Oct 9", selected: true },
  { kind: "idle", title: "Retry budget for the sync worker", sub: "mini · lantern", meta: "Oct 3" },
  { kind: "idle", title: "Rate limit headers in the API client", sub: "studio · tidewater", meta: "Sep 28" },
] as const;

export function HistoryStill() {
  const [ref, inView] = useInView<HTMLDivElement>();
  const reduced = useReducedMotion();
  const typeMs = useTokenMs("--m-type", 24);
  const staggerMs = useTokenMs("--m-stagger", 70);
  const start = inView || reduced;
  const typed = useSequence(query.length, typeMs, start, reduced);
  const rows = useSequence(sessions.length, staggerMs, start && typed >= query.length, reduced);

  return (
    <div ref={ref} className={styles.still} aria-hidden="true">
      <div className={styles.bar}><span>History</span><span className={styles.barMeta}>3 Macs</span></div>
      <div className={styles.searchWrap}><div className={styles.search}>{query.slice(0, typed)}</div></div>
      {sessions.map((s, i) => <Row key={s.title} {...s} shown={i < rows} />)}
    </div>
  );
}

const inbox = [
  { title: "Add rate limits to /login", sub: "mini · orbit", meta: "+42 −7", selected: true },
  { title: "Paginate the audit log", sub: "studio · tidewater", meta: "+118 −31" },
] as const;
const diff = [
  { cls: styles.del, text: "-    return handleLogin(w, r)" },
  { cls: styles.add, text: "+    if limited(ip) { tooMany(w); return }" },
  { cls: styles.add, text: "+    return handleLogin(w, r)" },
] as const;

export function ReviewStill() {
  const [ref, inView] = useInView<HTMLDivElement>();
  const reduced = useReducedMotion();
  const staggerMs = useTokenMs("--m-stagger", 70);
  // Steps 1..3 reveal the diff lines; the last step marks the first review done.
  const step = useSequence(diff.length + 1, staggerMs, inView || reduced, reduced);
  const done = step > diff.length;

  return (
    <div ref={ref} className={styles.still} aria-hidden="true">
      <div className={styles.bar}><span>Review</span><span className={styles.barMeta}>{done ? "2 ready" : "1 ready"}</span></div>
      {inbox.map((r, i) => <Row key={r.title} {...r} kind={i === 0 && !done ? "working" : "done"} />)}
      <pre className={styles.diff}>
        {diff.map((l, i) => (
          <span key={l.text} className={`${l.cls} ${i < step ? "" : styles.pending}`}>{l.text}</span>
        ))}
      </pre>
    </div>
  );
}
