"use client";
import { useCallback, useEffect, useState } from "react";
import styles from "./OneKey.module.css";
import { Tile } from "@/components/ui/Tile";
import { Pill } from "@/components/ui/Pill";
import { Kbd } from "@/components/ui/Kbd";
import { useInView, useKey, useReducedMotion } from "@/lib/motion";

/**
 * The visitor tries the app's key. The tile renders as working until the
 * section is in view, then waits for an approval; ⌘J focuses it, ⏎ allows.
 * Fixture text only; nothing here comes from a real session.
 */
type Phase = "working" | "waiting" | "focused" | "allowed" | "done";
const prompt = "Allow edit to 0042_users.sql?";
/** The pause between the "allowed" line and the agent's reply. */
const replyDelay = 400;

export function OneKeyTile() {
  const [ref, inView] = useInView<HTMLDivElement>();
  const reduced = useReducedMotion();
  const [phase, setPhase] = useState<Phase>("working");

  useEffect(() => {
    if (inView) setPhase((p) => (p === "working" ? "waiting" : p));
  }, [inView]);

  // The agent answers a beat after the approval lands.
  useEffect(() => {
    if (phase !== "allowed") return;
    const id = window.setTimeout(() => setPhase("done"), replyDelay);
    return () => window.clearTimeout(id);
  }, [phase]);

  const jump = useCallback(() => setPhase((p) => (p === "waiting" ? "focused" : p)), []);
  const allow = useCallback(() => {
    setPhase((p) => (p === "focused" ? (reduced ? "done" : "allowed") : p));
  }, [reduced]);
  const replay = useCallback(() => setPhase("waiting"), []);

  useKey("meta+j", phase === "waiting", jump);
  useKey("enter", phase === "focused", allow);

  const needsYou = phase === "waiting" || phase === "focused" || phase === "allowed";
  const answered = phase === "allowed" || phase === "done";

  return (
    <div ref={ref} className={styles.demo} data-phase={phase}>
      <div className={styles.bar}>
        <Pill mark={needsYou ? "needsYou" : "working"} kbd="⌘J">
          {needsYou ? (
            <span className={styles.count}>1 needs you</span>
          ) : (
            <span className={styles.count}>0 need you</span>
          )}
        </Pill>
      </div>
      <div className={styles.frame} data-focused={phase === "focused" || undefined}>
        <Tile
          name="users table"
          machine="studio"
          kind={needsYou ? "needsYou" : "working"}
          footer={needsYou ? prompt : undefined}
          className={styles.tile}
        >
          <span className="d">$ claude &quot;add a users table&quot;</span>{"\n"}
          <span className="d">read</span> migrations/{"\n"}
          <span className="w">write</span> migrations/0042_users.sql{"\n"}
          {answered ? <><span className="d">allowed</span>{"\n"}</> : null}
          {phase === "done" ? <><span className="g">ok</span> migration applied{"\n"}</> : null}
        </Tile>
      </div>
      {phase === "done" ? (
        <p className={styles.try}>
          That is the whole loop. Approvals first, then questions, then errors.{" "}
          <button type="button" className={styles.replay} onClick={replay}>Replay</button>
        </p>
      ) : (
        <p className={styles.try}>
          Try it: press{" "}
          <button type="button" className={styles.key} onClick={jump} aria-label="Press ⌘J">
            <Kbd>⌘J</Kbd>
          </button>{" "}
          to jump to it, then{" "}
          <button type="button" className={styles.key} onClick={allow} aria-label="Press Enter">
            <Kbd>⏎</Kbd>
          </button>{" "}
          to allow.
        </p>
      )}
    </div>
  );
}
