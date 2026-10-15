import styles from "./Terminal.module.css";
import { TerminalDemo } from "./TerminalDemo";

/** hesperctl reference (reference.go) and mcp (mcp.go) exist in relay/cmd/hesperctl; the skill is skills/hesper. */
const facts = [
  { label: "CLI", value: <><code>hesperctl reference</code> prints the whole CLI</> },
  { label: "MCP", value: <><code>hesperctl mcp</code> serves it over stdio</> },
  { label: "Skill", value: <><code>skills/hesper</code> teaches Claude Code and Codex to delegate and wait</> },
];

export function Terminal() {
  return (
    <section className="section" id="terminal">
      <div className="wrap">
        <h2>Everything the app does, hesperctl does too.</h2>
        <p className="lede">
          The CLI drives Hesper from a shell. The same commands are MCP tools for Claude Code and Codex,
          and an agent skill teaches them to fan work out to more agents.
        </p>

        <TerminalDemo />

        <dl className={styles.facts}>
          {facts.map((f) => (
            <div key={f.label} className={styles.fact}>
              <dt className={styles.label}>{f.label}</dt>
              <dd className={styles.value}>{f.value}</dd>
            </div>
          ))}
        </dl>
      </div>
    </section>
  );
}
