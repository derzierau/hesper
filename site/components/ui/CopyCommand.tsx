"use client";
import { useState } from "react";
import styles from "./CopyCommand.module.css";

/** A shell command with a copy button. Falls back to selecting the text when the clipboard is refused. */
export function CopyCommand({ lines, className = "" }: { lines: readonly string[]; className?: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const text = lines.join("\n");
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
    } catch {
      setState("failed");
      const sel = window.getSelection();
      const el = document.getElementById(id);
      if (sel && el) { sel.removeAllRanges(); const r = document.createRange(); r.selectNodeContents(el); sel.addRange(r); }
    }
    setTimeout(() => setState("idle"), 1600);
  }
  const id = `cmd-${lines[0].slice(0, 12).replace(/\W/g, "")}`;
  return (
    <div className={`${styles.box} ${className}`}>
      <pre className={styles.pre} id={id}>{lines.map((l) => <span key={l} className={styles.line}><span className={styles.prompt}>$ </span>{l}</span>)}</pre>
      <button type="button" className={styles.btn} onClick={copy} aria-live="polite">
        {state === "copied" ? "Copied" : state === "failed" ? "Select and copy" : "Copy"}
      </button>
    </div>
  );
}
