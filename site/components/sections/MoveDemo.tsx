"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import styles from "./Move.module.css";
import { Tile } from "@/components/ui/Tile";
import { Kbd } from "@/components/ui/Kbd";
import type { MarkKind } from "@/components/ui/StateMark";
import { useInView, useKey, useReducedMotion } from "@/lib/motion";

type Side = "laptop" | "mini";
type Phase = "pack" | "land" | "settle";
type Move = { to: Side; phase: Phase };
type TileState = "resting" | "faded" | "gone" | "hidden" | "landing";

/* Mirrors --m-move, --m-stagger and --m-settle in globals.css. */
const mMove = 180;
const mStagger = 70;
const mSettle = 220;
const autoDelay = 600;

const checklist = ["staged, unstaged, untracked", "same branch", "the conversation"];
const packDone = mMove + checklist.length * mStagger;

/** Fixture text only; nothing here comes from a real session. */
const beforeLines = (
  <>
    <span className="d">reading internal/api/routes.go</span>{"\n"}
    <span className="d">running go test ./internal/api/</span>
  </>
);
const afterLines = (
  <>
    <span className="d">resumed, same conversation</span>{"\n"}
    running go test ./internal/api/
  </>
);

const marks: Record<TileState, MarkKind> = {
  resting: "working", faded: "exited", gone: "exited", hidden: "working", landing: "starting",
};

function tileState(frame: Side, side: Side, move: Move | null): TileState {
  if (!move) return frame === side ? "resting" : "hidden";
  if (frame === side) return move.phase === "settle" ? "gone" : "faded";
  return move.phase === "pack" ? "hidden" : "landing";
}

/**
 * The move, driven by the visitor: ⇧⌘M continues the agent on the mini, ⌘Z
 * brings it back. It also plays once on its own when the demo comes into
 * view. Both tiles stay in the DOM; classes toggle visibility, opacity and a
 * 6 px rise. Under reduced motion the finished state renders at once.
 */
export function MoveDemo() {
  const reduced = useReducedMotion();
  const [ref, inView] = useInView<HTMLDivElement>();
  const [side, setSide] = useState<Side>("laptop");
  const [move, setMove] = useState<Move | null>(null);
  const [arrived, setArrived] = useState<Record<Side, boolean>>({ laptop: false, mini: false });
  const sideRef = useRef<Side>("laptop");
  const busy = useRef(false);
  const played = useRef(false);
  const timers = useRef<number[]>([]);

  useEffect(() => {
    const pending = timers.current;
    return () => pending.forEach((id) => window.clearTimeout(id));
  }, []);

  const startMove = useCallback(
    (to: Side) => {
      if (busy.current || to === sideRef.current) return;
      sideRef.current = to;
      setArrived((a) => ({ ...a, [to]: true }));
      if (reduced) {
        setSide(to);
        return;
      }
      busy.current = true;
      const later = (ms: number, fn: () => void) => timers.current.push(window.setTimeout(fn, ms));
      setMove({ to, phase: "pack" });
      later(packDone, () => setMove({ to, phase: "land" }));
      later(packDone + mSettle, () => setMove({ to, phase: "settle" }));
      later(packDone + mSettle + mMove, () => {
        setSide(to);
        setMove(null);
        busy.current = false;
      });
    },
    [reduced],
  );
  const toMini = useCallback(() => startMove("mini"), [startMove]);
  const toLaptop = useCallback(() => startMove("laptop"), [startMove]);

  useEffect(() => {
    if (!inView || played.current) return;
    played.current = true;
    timers.current.push(window.setTimeout(toMini, autoDelay));
  }, [inView, toMini]);

  useKey("meta+shift+m", inView, toMini);
  useKey("meta+z", inView, toLaptop);

  const frame = (which: Side) => {
    const state = tileState(which, side, move);
    return (
      <div className={styles.frame}>
        <p className={styles.frameLabel}>{which}</p>
        <div className={styles.slot}>
          <Tile name="api" machine={which} kind={marks[state]} className={`${styles.tileWrap} ${styles[state]}`}>
            {arrived[which] ? afterLines : beforeLines}
          </Tile>
        </div>
      </div>
    );
  };

  return (
    <>
      <div
        ref={ref}
        className={styles.demo}
        role="figure"
        aria-label="A move: the agent on the laptop, the sealed checkpoint it sends, the same agent on the mini."
      >
        {frame("laptop")}

        <div className={styles.connector}>
          <div className={`${styles.packet} ${move ? styles.active : ""}`}>
            <p className={styles.seal}>
              <svg width="7" height="7" viewBox="0 0 7 7" aria-hidden="true">
                <rect width="7" height="7" rx="1" fill="var(--working)" />
              </svg>
              sealed
            </p>
            <ul className={styles.list}>
              {checklist.map((line, i) => (
                <li key={line} className={styles.item} style={{ "--i": i } as React.CSSProperties}>
                  <span className={styles.box} aria-hidden="true"><span className={styles.fill} /></span>
                  {line}
                </li>
              ))}
            </ul>
            <p className={styles.never}>never your branches or index</p>
          </div>
        </div>

        {frame("mini")}
      </div>

      <p className={styles.prompt}>
        Try it: press{" "}
        <button type="button" className={styles.key} onClick={toMini} aria-label="Continue on the mini">
          <Kbd>⇧⌘M</Kbd>
        </button>{" "}
        to continue on the mini,{" "}
        <button type="button" className={styles.key} onClick={toLaptop} aria-label="Move it back">
          <Kbd>⌘Z</Kbd>
        </button>{" "}
        to move it back.
      </p>
    </>
  );
}
