"use client";
import styles from "./Terminal.module.css";
import { useInView, useReducedMotion, useSequence } from "@/lib/motion";
import { useTokenMs } from "./useTokenMs";

/**
 * The hesperctl session, typed when it comes into view. Comment lines appear
 * whole, commands type character by character after a "$ " prompt, and each
 * command gets its fixture output. Under reduced motion everything is visible.
 *
 * Every flag here exists in relay/cmd/hesperctl: new --machine --worktree
 * (agents.go), wait --until needs-you (agents_more.go), mcp (mcp.go); --mcp is
 * install.sh's flag. The output lines are fixtures.
 */
type Line = { kind: "comment" | "command" | "output"; text: string };
const script: readonly Line[] = [
  { kind: "comment", text: "# start an agent on another Mac, in its own worktree" },
  { kind: "command", text: 'hesperctl new --machine mini --worktree "add rate limits to /login"' },
  { kind: "output", text: "mini/a7f3k2  api  starting" },
  { kind: "comment", text: "# block until it needs you" },
  { kind: "command", text: "hesperctl wait a7f3k2 --until needs-you" },
  { kind: "output", text: "a7f3k2  needs-you  Allow edit to 0042_users.sql?" },
  { kind: "comment", text: "# the same commands as MCP tools" },
  { kind: "command", text: "./install.sh --mcp" },
  { kind: "output", text: "registered hesper with claude and codex" },
];

export function TerminalDemo() {
  const [ref, inView] = useInView<HTMLPreElement>();
  const reduced = useReducedMotion();
  const typeMs = useTokenMs("--m-type", 24);
  const staggerMs = useTokenMs("--m-stagger", 70);
  const settleMs = useTokenMs("--m-settle", 220);

  // One clock ticks every --m-type. A whole line waits a stagger (or a settle,
  // for output, as if the command ran) before it shows; a command waits a
  // stagger for its prompt and then one tick per character.
  const pause = Math.max(1, Math.round(staggerMs / typeMs));
  const run = Math.max(1, Math.round(settleMs / typeMs));
  const units = script.map((l) => (l.kind === "command" ? pause + l.text.length : l.kind === "output" ? run : pause));
  const total = units.reduce((a, b) => a + b, 0);
  const progress = useSequence(total, typeMs, inView || reduced, reduced);

  let before = 0;
  const lines = script.map((l, i) => {
    const shown = Math.min(Math.max(progress - before, 0), units[i]);
    before += units[i];
    return { ...l, shown, done: shown >= units[i] };
  });

  return (
    <pre ref={ref} className={styles.term}>
      {lines.map((l, i) => {
        if (l.kind !== "command") {
          return <span key={i} className={`${styles.line} ${l.kind === "comment" ? styles.comment : ""} ${l.done ? "" : styles.pending}`}>{l.text}</span>;
        }
        const typed = Math.max(l.shown - pause, 0);
        const outputPending = !(lines[i + 1]?.done ?? true);
        const caret = l.shown >= pause && outputPending;
        return (
          <span key={i} className={`${styles.line} ${l.shown >= pause ? "" : styles.pending}`}>
            <span className={styles.prompt}>$ </span>
            {l.text.slice(0, typed)}
            {caret && <span className={styles.caret} />}
            <span className={styles.pending}>{l.text.slice(typed)}</span>
          </span>
        );
      })}
    </pre>
  );
}
