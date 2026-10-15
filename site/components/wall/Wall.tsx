"use client";
import { useEffect, useRef } from "react";
import { Tile } from "@/components/ui/Tile";
import { Pill } from "@/components/ui/Pill";
import type { MarkKind } from "@/components/ui/StateMark";
import { useInView, useReducedMotion, useSequence } from "@/lib/motion";
import styles from "./Wall.module.css";

/** One terminal line. Classes d, g and w are the Tile's dim, green and indigo. */
type Line = { c?: "d" | "g" | "w"; text: React.ReactNode };
type Agent = { name: string; machine: string; kind: MarkKind; lines: Line[] };

/** Six fixture agents on three Macs, in boot order. Nothing here comes from a real session. */
const agents: Agent[] = [
  { name: "api", machine: "mini", kind: "working", lines: [
    { c: "d", text: "$ go test ./internal/..." },
    { text: "ok  internal/protocol  0.41s" },
    { text: "ok  internal/vt      1.20s" },
    { c: "w", text: "▸   internal/vt" },
  ] },
  { name: "migrations", machine: "laptop", kind: "working", lines: [
    { c: "d", text: "$ claude" },
    { text: "Reading migrations/" },
    { text: "Found 41 files" },
    { c: "w", text: "▍ Writing 0042_users.sql" },
  ] },
  { name: "docs", machine: "studio", kind: "done", lines: [
    { c: "d", text: "$ git commit -m \"docs\"" },
    { text: "[main 3f2a1c9] docs: setup" },
    { text: " 2 files changed" },
    { c: "g", text: "✓ done" },
  ] },
  { name: "wall-perf", machine: "studio", kind: "question", lines: [
    { text: "Which approach?" },
    { c: "w", text: "❯ 1. Cache row heights" },
    { text: "  2. Skip offscreen tiles" },
    { text: "  3. Both" },
  ] },
  { name: "review-ux", machine: "mini", kind: "working", lines: [
    { c: "d", text: "$ swift test Review" },
    { text: "Suite ReviewTests started" },
    { c: "g", text: "✔ testInboxOrder (0.02s)" },
    { c: "w", text: "▸ testDiffScroll" },
  ] },
  { name: "shell", machine: "laptop", kind: "idle", lines: [
    { c: "d", text: "$ hesperctl ls" },
    { c: "d", text: <span className={styles.cursor} /> },
  ] },
];

const NEEDS_PROMPT = "Allow edit to 0042_users.sql?";
const NEEDS_INDEX = agents.findIndex((a) => a.name === "migrations");

/*
 * The boot sequence runs on one clock: a tick is --m-type (24 ms). A tile's
 * mark fills to working on its start tick, each of its lines lands LINE
 * ticks after the previous one (96 ms), its final mark is set with its last
 * line, and the next tile starts STAGGER ticks later (72 ms, --m-stagger).
 * Six tiles, 22 lines: the last line lands at tick 104, about 2.5 s. Then,
 * NEEDS ticks (1.2 s) later, migrations asks for an approval.
 */
const TICK_MS = 24;
const LINE = 4;
const STAGGER = 3;
const NEEDS = 50;

const starts: number[] = [];
let tick = 1;
for (const a of agents) {
  starts.push(tick);
  tick += a.lines.length * LINE + STAGGER;
}
const lastLine = tick - STAGGER;
const needsAt = lastLine + NEEDS;

/**
 * The one orchestrated moment on the page. The wall renders at rest, all six
 * tiles starting with empty terminals, until it is in view; then the agents
 * boot one after another, and a beat after the last one "migrations" needs
 * you: its footer settles in, the pill goes 0 → 1 and the page hears about
 * it. Under reduced motion the finished state renders at once.
 */
export function Wall() {
  const [ref, inView] = useInView<HTMLDivElement>();
  const reduced = useReducedMotion();
  const step = useSequence(needsAt, TICK_MS, inView, reduced);
  const needs = step >= needsAt;

  // Told once, so the nav can answer.
  const told = useRef(false);
  useEffect(() => {
    if (!needs || told.current) return;
    told.current = true;
    window.dispatchEvent(new CustomEvent("hesper:needs-you"));
  }, [needs]);

  const count = needs ? 1 : 0;

  return (
    <div ref={ref} className={styles.panel} aria-label="Example wall with six agents">
      <div className={styles.grid}>
        {agents.map((a, i) => {
          const start = starts[i];
          const landed = Math.max(0, Math.min(a.lines.length, Math.floor((step - start) / LINE)));
          const booted = landed === a.lines.length;
          const asks = i === NEEDS_INDEX && needs;
          const kind: MarkKind = asks ? "needsYou" : step < start ? "starting" : booted ? a.kind : "working";
          return (
            <Tile
              key={a.name}
              name={a.name}
              machine={a.machine}
              kind={kind}
              footer={asks ? NEEDS_PROMPT : undefined}
              className={asks ? styles.rise : ""}
            >
              {a.lines.slice(0, landed).map((l, j) => (
                <span key={j} className={`${styles.line} ${l.c ?? ""}`}>{l.text}</span>
              ))}
            </Tile>
          );
        })}
      </div>
      <div className={styles.footer}>
        <span className={styles.count}>6 agents on 3 Macs</span>
        <Pill mark={count ? "needsYou" : "working"} kbd="⌘J">{count} {count === 1 ? "needs" : "need"} you</Pill>
      </div>
    </div>
  );
}
