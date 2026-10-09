# Turn agent review into trust calibration

Hesper should build review around one main idea. The reviewer's job is no longer only to find bugs in a diff. It is to decide, fast and with calibrated trust, whether each agent's work is true to the request: accept it, discard it, or send it back. So the surface should lead with a falsifiable claim of intent. It should order the change by risk and dependency, not alphabetically. It should show evidence that can be re-run, record what the human has and has not looked at, and send anchored comments back to the agent as one batched instruction. The research supports this direction, though unevenly. File order alone changes defect detection: in one experiment, the odds of finding a defect were **64% lower** when its file came last. Complexity combined with an "AI-authored" label drives over-acceptance of incorrect code (**n=385**). AI-flagged hotspots narrow where reviewers look without catching more severe bugs. Experienced developers were **19% slower** with early-2025 AI tools and still believed they were faster. On the product side, the field has converged on few, high-confidence findings and AI-driven grouping of hunks. Still, every shipping tool reviews parallel agents one branch at a time, and none maps plan steps to hunks. That leaves a clear opening for a native, fleet-level review inbox. On rendering, the fastest tools use the same recipe, and it ports directly to AppKit: one virtualized row stream for all files, code geometry kept separate from variable-height blocks, plain text first with highlighting filled in off the main thread, and a histogram line diff refined per hunk. Avoid TextKit 2 and SwiftUI `List` for the diff body. Much remains unproven. No controlled study has yet measured review accuracy on multi-file agent changes with and without summaries, ordering or risk cues. So Hesper should treat each enrichment as a hypothesis and instrument it.

## Review now has four jobs, and line-reading is the weakest of them

Classic code review research describes an activity that agent output is already outgrowing. Google's 2018 case study of about 9M changes found that **about 90% of reviews touch fewer than 10 files** and a typical change is about 24 lines ([Google Research](https://research.google/pubs/modern-code-review-a-case-study-at-google/)). The 200–400 LOC-per-session heuristics trace back to a single vendor-funded Cisco study ([SmartBear](https://smartbear.com/learn/code-review/best-practices-for-peer-code-review/)). Agent changes routinely exceed both scales, and an agent queue adds a scenario no study covers: many sequential reviews in one sitting. Even so, the older work holds three findings that transfer cleanly.

**Presentation is part of reviewability.** Baum et al. showed that how a change is presented affects how hard it is to review, not just its size ([overview](https://www.awesomecodereviews.com/research/code-review-research-overview/)). Working-memory capacity predicted detection of *delocalized* defects, meaning defects that span several places, but not other defect types ([UZH](https://www.zora.uzh.ch/entities/publication/245eb643-3ebd-476c-a613-8097ff6d9582)).

**Order is a hidden variable.** Fregnan et al. analyzed **219,476 PRs** and found that earlier files get more comments, even after controlling for confounders. In their experiment, one seeded defect type had 64% lower odds of detection when its file came last. **72.6%** of participants denied that position affected them ([arXiv 2208.04259](https://arxiv.org/pdf/2208.04259)). Only **10.2%** of 1,355 developers surveyed for ICSE 2026 consider alphabetical order optimal, and 66% want order to be customizable ([ICSE 2026](https://conf.researchr.org/details/icse-2026/icse-2026-research-track/310/Breaking-the-Alphabet-Rethinking-File-Ordering-in-Code-Review)). No single order wins in every context, though ([arXiv 2506.10654](https://arxiv.org/pdf/2506.10654)).

**Decomposition cuts noise, not misses.** Reviewers of untangled changes reported **fewer false positives** but found no more defects ([PeerJ CS](https://peerj.com/articles/cs-193/)). More than 90% of tangled commits involve three or fewer concerns ([arXiv 2601.21298](https://arxiv.org/pdf/2601.21298)).

The AI-specific evidence changes the goal itself. Singh, Sovrano, Hellendoorn and Bacchelli (FSE 2026, **385 participants**) found that complexity raised acceptance of *incorrect* revisions. Provenance had no main effect, but under high complexity, wrong code labelled AI-authored was accepted substantially more often ([FSE 2026](https://conf.researchr.org/details/fse-2026/fse-2026-research-papers/111/The-Interaction-of-Complexity-and-Provenance-in-Code-Review-Decisions-Evidence-from-)). Tufano et al. gave 29 professionals a GPT-4 review draft as a starting point. Their median detection of injected issues stayed at **50%**. They covered fewer distinct lines (371 vs 484 manually), took no less time, and the extra issues they found were mostly low-severity ([arXiv 2411.11401](https://arxiv.org/html/2411.11401)). In METR's RCT, experienced developers were **19% slower** with AI and afterwards still believed they had been 20% faster. The time went into reviewing "directionally correct" output ([METR via Harvard](https://tagteam.harvard.edu/hub_feeds/3997/feed_items/14760086/about)). That output is the "almost right, but not quite" problem **66%** of Stack Overflow's 2025 respondents report ([InfoWorld](https://infoworld.com/article/4031673/ai-use-among-software-developers-grows-but-trust-remains-an-issue-stack-overflow-survey.html)). Sonar's survey found that **96%** don't fully trust AI code, yet only **48%** always check it before committing ([Sonar](https://www.sonarsource.com/state-of-code-developer-survey-report.pdf)). In a student study, low-effort agent use (auto-accepted edits) went with worse comprehension and a reduced ability to extend the code later ([arXiv 2607.26375](https://arxiv.org/abs/2607.26375)).

Taken together, these findings define what Hesper's review should accomplish. It has four jobs, and each needs a different instrument:

| Job | What it means | Instrument |
|---|---|---|
| Alignment | Did the agent do what was asked, and nothing else? | Intent-to-hunk mapping |
| Correctness | Is the work right, especially in complex or delocalized logic? | Risk-ordered reading |
| Verification | What actually ran, and what did it cover? | Re-runnable evidence |
| Comprehension | Does the human still understand the codebase well enough to steer the next agent? | Walkthroughs and grouping |

Line-by-line inspection serves only the second job, and only partly. The JetBrains/Lund participatory study names the problem: agents present output of uneven quality with uniform confidence, so reviewers "fall back to reading every line" ([arXiv 2606.01969](https://arxiv.org/pdf/2606.01969)). Hesper's review should replace that fallback with calibrated reading: scrutiny where complexity and risk concentrate, and explicit evidence or a skim elsewhere.

## The market fixed noise and grouping, but still reviews one branch at a time

Today's tools fall into three groups:

| Group | Tools | How review works |
|---|---|---|
| In-editor accept/reject of local agent edits | Cursor, Zed, Windsurf/Devin Desktop, Warp | Per-hunk keep or reject |
| AI reviewers posting on PRs | Bugbot, Copilot, Claude Code Review, Codex, CodeRabbit, Graphite, Devin Review | Comments, fixes, dismissals |
| Parallel-agent workspaces | Conductor, Sculptor, Cursor 2.0, Codex app, GitHub Mission Control | One diff per isolated worktree or container |

The PR-side group has converged on a clear lesson: **silence is a feature**. Codex reports only P0 and P1 issues ([OpenAI](https://developers.openai.com/codex/integrations/github)). Claude Code Review runs parallel agents, verifies findings to drop false positives, ranks them by severity, and never approves ([Claude blog](https://claude.com/blog/code-review)). Copilot stays quiet in 29% of its 60M reviews ([GitHub](https://github.blog/ai-and-ml/github-copilot/60-million-copilot-code-reviews-and-counting/)). Bugbot turns reactions and replies into more than 44k learned rules, and disables rules that keep drawing negative signal ([Cursor](https://cursor.com/blog/bugbot-learning)). The opposite failure is well documented. CodeRabbit is most often criticized for nitpicks and for labelling trivia "major" on large PRs ([CuratorBits](https://curatorbits.com/reviews/coderabbit/)). Meta's first interface for AI-suggested fixes made reviewers **more than 5% slower** until suggestions went only to authors ([overview](https://www.awesomecodereviews.com/research/code-review-research-overview/)). Verbose LLM feedback can add cognitive load ([overview](https://www.awesomecodereviews.com/research/code-review-research-overview/)).

The second convergence is **reordering the diff for a human reading it**:

- **Devin Review** groups hunks into logical units with an explanation each, and renders moved or renamed code as a move rather than a delete plus an insert ([Cognition](https://cognition.com/blog/devin-review)).
- **CodeRabbit's Change Stack** (May 2026) splits a PR into independent "cohorts" of ordered layers. Each layer is anchored to line ranges, and a right rail tracks the code in view. Sequence, state or ER diagrams appear only when they help ([CodeRabbit](https://coderabbit.ai/blog/introducing-atlas-the-first-ai-native-code-review-interface)).
- **Amp** shows a recommended review order with churn indicators per file ([Amp](https://ampcode.com/news/review)).
- **Copilot** puts the agent's session logs beside the diff and links each commit to its log ([GitHub changelog](https://github.blog/changelog/2025-10-28-a-mission-control-to-assign-steer-and-track-copilot-coding-agent-tasks/)).
- **Warp** lets reviewers batch inline comments and send them back to the agent in one pass, including to Claude Code and Codex ([Warp](https://docs.warp.dev/code/code-review/interactive-code-review)).

Three gaps matter for Hesper.

**The parallel unit of review is still "one branch, one diff, reviewed alone."** Conductor reviews and merges one workspace at a time ([Conductor](https://www.conductor.build/docs/concepts/parallel-agents)). Sculptor pairs one container at a time into the local IDE ([Imbue](https://imbue.com/blog/sculptor-announce)). Cursor runs up to 8 agents on one prompt but offers no ranked triage across them ([Cursor 2.0](https://cursor.com/changelog/2-0)). No tool found ranks finished agent tasks by risk or size, or supports bulk approve, discard or send-back across a fleet.

**No tool maps plan steps to hunks.** Copilot links the whole session log. Jules gates on an editable plan, but the plan is not linked to the resulting diff afterwards ([sinatra.dev](https://www.sinatra.dev/blog/google-jules-review)).

**In-editor review surfaces are fragile.** Cursor, Zed and Windsurf have all shipped regressions that lost the accept/reject UI ([Cursor forum](https://forum.cursor.com/t/review-diff-navigation-bar-missing-after-agent-changes-v2-6-20/155587); [Zed #50142](https://github.com/zed-industries/zed/issues/50142); [Windsurf #131](https://github.com/Exafunction/codeium/issues/131)). That suggests review is a secondary concern in those products. It also suggests that a tool treating review as its main surface has room to win on reliability alone.

Every published metric measures the AI reviewer, not the human. Examples are Bugbot's 78% resolution rate, judged by an LLM on public repos, and Claude's under-1% of findings marked incorrect. **No vendor publishes human time-to-review for agent PRs.**

## Five enrichments, ranked by how much the evidence supports them

Hesper sits upstream of the PR: it owns the terminal, the transcript, the checkpoints and the history. So it can attach context that PR-side tools must reconstruct after the fact. The table below ranks the candidate enrichments by how much evidence supports them, not by how impressive they look.

| Enrichment | Evidence for review benefit | Cheapest trustworthy source in Hesper | Design rule |
|---|---|---|---|
| Risk- and dependency-ordered reading | **Strong** (order bias experiment) | Symbol graph, churn, hotspot data | Never alphabetical; reviewer can reorder |
| Grouping into 2–3 concerns | **Moderate** (fewer false positives) | LLM grouping over hunks | Few groups beat fine-grained untangling |
| Re-runnable behavioral evidence | **Practitioner demand**, no accuracy study | Test runs captured in the tile, CI artifacts | Prefer diffable artifacts to agent assertions |
| Intent summary and walkthrough | **Weak to moderate** (observational) | Agent's own summary, plan, user prompt | Label it "agent's claim"; link it to hunks |
| Calibrated risk flags | **Mixed**: attention-narrowing risk | Deterministic signals first, LLM second | Frame flags as "also look here"; track what went unseen |

**Intent should be presented as a claim, not a caption.** AI-written PR descriptions were associated with lower median review time (12.2 h vs 16.1 h) and higher merge odds (OR 1.57). The study was observational, and developers often edited the text ([arXiv 2402.08967](https://arxiv.org/pdf/2402.08967)). In a study of explainable AI, inline explanations with highlighting raised trust *and* produced more rejections than feedback alone, a sign of more scrutiny. But the AI in that study was always correct, so it did not test over-reliance ([arXiv 2607.24601](https://arxiv.org/html/2607.24601v1)). For Hesper, the agent's summary is part of what is under review. Each sentence of the summary should link to the hunks that implement it. Hunks that no sentence mentions should be flagged as "unmentioned changes." That flag is the cheapest detector of scope creep and agent misalignment, which are rejection causes the AIDev studies document ([arXiv 2602.04226](https://arxiv.org/html/2602.04226v1)).

Where the user wrote a spec, it should drive a checklist. Kiro's EARS acceptance criteria ("WHEN … THE SYSTEM SHALL …") are an example ([Kiro](https://kiro.dev/docs/specs/feature-specs/requirements-first/)). Each criterion should need a passing test or an evidence artifact. Phabricator's mandatory "test plan" field was the precedent ([Graphite](https://graphite.com/guides/differential-phabricators-code-review-application)).

**Evidence should be re-runnable rather than narrated.** Practitioners validate agent work mainly by running tests. Huang et al. found experienced developers "control" agents through meticulous verification, and keep them off business logic ([summary](https://www.emergentmind.com/papers/2512.14012)). Cursor's cloud agents now attach screenshots, videos and logs from their own VMs ([Cursor](https://cursor.com/blog/agent-computer-use)). Staff concede that broken Playwright videos are a known class of bug ([forum](https://forum.cursor.com/t/getting-video-demos-out-of-cloud-agents/157217)), and Copilot's screenshot attachment is "not a stable, always-on feature" ([GitHub discussion](https://github.com/orgs/community/discussions/169913)). The mature forms are all deterministic diffs against a baseline:

- Chromatic separates "did anything change visually" from "is the change intended" ([Chromatic](https://www.chromatic.com/docs/test)).
- CodSpeed tables base and head benchmark times ([example](https://github.com/CodSpeedHQ/codspeed-rust/pull/138)).
- oasdiff fails CI on API breaking changes ([CodeRabbit](https://docs.coderabbit.ai/tools/oasdiff)).
- Codecov reports patch coverage of changed lines ([Codecov](https://docs.codecov.com/docs/pull-request-comments)).

Hesper has a structural advantage here: it watched the terminal. It can extract the actual test commands, exit codes and failures from the agent's tile, mark which changed lines a run covered, and offer a "re-run on this Mac" button. Showing proof that was observed, rather than claimed, is something PR-side tools cannot do.

**Risk cues should start deterministic and stay humble.** The Tufano result is the central warning: highlights pull attention onto flagged lines (371 vs 484 lines covered) without improving detection of severe issues. Traffic-light judges would recreate that anchoring unless they are calibrated. The cheap, deterministic signals are:

- **Fan-in of changed symbols.** Greptile and CodeRabbit's blast-radius graphs ship this kind of signal ([Greptile](https://www.greptile.com/docs/how-greptile-works/graph-based-codebase-context)).
- **Hotspot and code-health deltas** ([CodeScene](https://docs.enterprise.codescene.io/latest/guides/delta/automated-delta-analyses.html)).
- **Patch coverage** of changed lines.
- **Absent change coupling:** files that usually change together but didn't this time. CodeScene flags it, and it is especially relevant to agents, which forget companion edits such as migrations, docs or tests.

LLM effort scores, like CodeRabbit's 1–5, ship but have not been validated against defect outcomes ([CodeRabbit](https://docs.coderabbit.ai/pr-reviews/walkthroughs)). The complexity-plus-AI-label finding argues for a specific rule. Raise scrutiny on hunks that are complex *and* lack evidence, rather than applying uniform suspicion. Always show a coverage ribbon of what the reviewer has not yet looked at, so unflagged never reads as safe.

**Provenance becomes useful once it reaches line level.** Git AI stores per-line agent, model and prompt attribution in git notes, and `git ai blame` jumps from a line to the prompt behind it ([Git AI](https://usegitai.com/docs/how-git-ai-works)). Entire CLI captures Claude Code sessions through hooks and stores checkpoints on a side branch ([Entire](https://entire.io/blog/the-entire-cli-how-it-works-and-where-its-headed)). Hesper's checkpoints and shared history already hold this data. The review action this enables is "why is this hunk here?". It should open the transcript at the tool call that wrote the hunk, along with the user message that preceded it. One caveat applies: agent reasoning can itself be confabulated ([arXiv 2606.01969](https://arxiv.org/pdf/2606.01969)). So the transcript is supporting context, never evidence.

**Structural diffing removes the noise agents create.** Agents reformat and relocate code. Difftastic's tree-sitter diff suppresses formatting churn ([difftastic](https://github.com/Wilfred/difftastic)). SemanticDiff marks a block "moved without changes" and offers a compare-with-original view for edits made during a move ([SemanticDiff](https://semanticdiff.com/docs/understand-diff/moved-code/)). Git's `--color-moved=dimmed-zebra` provides a move signal at almost no cost ([git list](https://public-inbox.org/git/20180716230542.81372-7-sbeller@google.com/)). Major forges still lack move rendering ([GitLab 517156](https://gitlab.com/gitlab-org/gitlab/-/work_items/517156)), so a "formatting-only" badge and dimmed moves would be visible differentiators. Difftastic's `--check-only` mode compares syntax trees without computing a diff, which makes it a fast way to decide when to show that badge ([difft man](https://man.archlinux.org/man/difft.1.en)).

## Render one row stream on AppKit and Metal, never on TextKit 2 or SwiftUI List

The engineering write-ups from the fastest review surfaces all describe the same architecture.

**One stream for the whole review.** Zed shows the entire project diff as one multibuffer. It builds split view from the same rows plus alignment spacers inserted by its block map, the component that also handles inline diagnostics, and optimized it for thousands of changed files ([Zed](https://zed.dev/blog/split-diffs)).

**Geometry kept separate from dynamic blocks.** GitHub's Copilot-app renderer handled a PR with about 2,200 files, over 1M changed lines and 400+ comments by mounting only about 100 recycled rows. It keeps code geometry (deterministic, in typed-array prefix sums) separate from dynamic block heights such as comments, which are fingerprinted by content and width bucket. It measures in idle batches within about 2,400 px of the viewport and never corrects during active scroll. It streams the file tree before content and shows plain text before off-thread highlighting arrives ([GitHub](https://github.blog/2024-09-18-rendering-huge-pull-requests-in-the-github-copilot-app/)). GitHub's web rewrite cut input latency on 10k-line split diffs from about **450 ms to about 100 ms**, and to 40–80 ms with virtualization. The main levers were one delegated event handler, O(1) comment maps keyed by path and line, and pure line renderers ([GitHub](https://github.blog/engineering/architecture-optimization/the-uphill-climb-of-making-diff-lines-performant/)). The team asserted mounted-row counts and scroll-correction size as test budgets. Hesper should adopt that practice as headless performance tests.

On macOS, the text foundation is the decision with the most risk.

**TextKit 2 estimates document height, so the scroller jumps.** Apple calls this "as-designed." STTextView's author concludes it is "not a silver bullet" ([Krzyżanowski](https://blog.krzyzanowskim.com/2025/08/14/textkit-2-the-promised-land/)). Developers report degradation above about 3,000 lines on macOS ([Apple forums](https://developer.apple.com/forums/thread/729491)).

**SwiftUI `List` builds every row.** On macOS it initializes all rows, even with only about 15 visible, and becomes unusable at 10k–30k rows ([troz.net](https://troz.net/post/2024/swiftui_lists/)).

That leaves two viable foundations:

| | View-based `NSTableView` | Custom Metal view (GPUI-style) |
|---|---|---|
| How it draws | Fixed-height monospaced rows drawn with cached `CTLine`s, plus variable-height comment rows | CoreText shaping cached per run, alpha glyph atlas, one instanced draw per run, SDF rounded rectangles for hunk backgrounds ([Zed](https://zed.dev/blog/videogame)) |
| Selection and VoiceOver | Nearly free | Must be built |
| Ceiling | Lower | Highest polish; the "beautiful" option |

Zed's 120 fps lessons apply directly to the Metal option: pool instance buffers, keep presenting for about 1 s after input so ProMotion does not downclock, drive frames from the display link, and turn off `presentsWithTransaction` in steady state ([Zed](https://zed.dev/blog/120fps)).

A pragmatic path is NSTableView first, with a protocol boundary so a Metal renderer can replace the code rows later. SwiftUI should handle only chrome: sidebar, inspector and toolbar. libghostty is tempting because Hesper already renders terminals, but its stable embeddable piece is the VT parser ([Ghostty](https://github.com/ghostty-org/ghostty)). A terminal grid also cannot host per-hunk controls or comment composers. Rendering `delta` output in a tile is a reasonable stopgap, not the destination.

The diff pipeline should have budgets at every stage:

1. Intern lines to integer IDs.
2. Run a histogram line diff on a background queue, for example imara-diff through a Rust static library. Histogram is preferred for readability ([Nugroho et al.](https://arxiv.org/abs/1902.02467v3)), and imara-diff reports it 10–100% faster than its own Myers implementation ([imara-diff](https://docs.rs/crate/gix-imara-diff/0.2.4/source/README.md)).
3. Refine only modified hunks with similar old and new line counts to word or token level. Skip pure inserts, pure deletes and huge changes, as imara-diff advises.
4. Cap work per hunk, as VS Code does with its 5 s character-diff limit, and say so visibly when a hunk falls back to line-only highlighting ([vscode-diff](https://cdn.jsdelivr.net/npm/vscode-diff@3.0.1/README.md)).
5. Run structural diffing only as an optional tier. Difftastic documents poor scaling and high memory on files with many changes.

Speed in the interaction comes from **auto-advance and one-key decisions**. Zed's "stage and next" (`cmd-y`) makes review one keystroke per hunk ([Zed](https://zed.dev/docs/git.md)). GitLab binds "viewed" to `v` ([GitLab](https://docs.gitlab.com/ee/user/shortcuts/)), and Graphite toggles the file tree with `F` ([Graphite](https://graphite.com/docs/review-pull-requests)). A Hesper grammar might look like this:

| Key | Action |
|---|---|
| `n` / `p` | Next or previous hunk |
| `]f` / `[f` | Next or previous file |
| `a` / `r` | Accept or reject the hunk, then advance |
| `v` | Mark the file viewed, then jump to the next unviewed file |
| `c` | Comment |
| `u` | Undo the last decision |
| `?` | Show all shortcuts |

Around those keys, the review should show a global progress meter ("N of M hunks decided"), a minimap strip marking risk, comments and not-yet-viewed regions, and per-file viewed state keyed to a hash of that file's diff. The viewed state then resets only when that file actually changes.

Comments should be anchored to file, side, line range and diff hash, as GitHub does. A batch of comments should go back to the agent's tile as one structured instruction, as Warp does. The agent's revisions should then come back as fresh hunks in the same stream, with earlier decisions preserved. No source has measured how much speed auto-advance or minimaps actually buy, so these choices are well-supported conventions, not proven gains.

## Hesper's opening is the fleet inbox, which no tool has built

Hesper's position differs from every product surveyed. It runs many agents across several Macs, with checkpoints and shared history, so its natural unit of review is the **fleet**, not the PR. The design that follows from the evidence has five parts.

**A review inbox across all agents.** It would list finished or paused work from every tile on every Mac, ranked by a transparent score: size, fan-in, absent coupling, evidence status, and whether the agent's claimed intent covers every hunk. It would allow bulk triage: approve, discard, send back with notes, or move to another Mac. GitHub Mission Control, Devin's review dashboard and the Codex Automations queue gesture at this. The Codex queue is documented only by third parties ([Vaughan](https://codex.danielvaughan.com/2026/04/08/codex-desktop-automations/)). None of them ranks or bulk-triages. Meta's "Next Reviewable Diff" flow, which raised review actions per day by **17%** ([Meta](https://engineering.fb.com/2022/11/16/culture/meta-code-review-time-improving/)), is the closest evidence that a well-ordered queue pays off.

**Side-by-side review of parallel attempts.** When several agents tackle the same prompt, the reviewer should compare their approaches, not read three diffs in turn. That means aligning attempts by the intent statements they share, showing where they diverge, and letting the reviewer take hunks from different attempts. Cursor runs best-of-N, but no tool found offers a structured comparison.

**Checkpoints as an automatic stack.** GitHub's native stacked PRs (public preview since July 2026) ([GitHub](https://github.blog/changelog/2026-07-30-stacked-pull-requests-are-now-in-public-preview/)) and Graphite's guidance to review each layer on its own are author-side habits that agents lack. Hesper can present an agent's checkpoints as a reviewable stack. Combined with LLM grouping into two or three concerns, this gives reviewers decomposition without asking the agent to rewrite its history.

**A record of attention.** Meta used "eyeball time" as a guard against rubber-stamping but published no numbers. The research shows reviewers neither notice their own order bias nor resist the pull of AI flags. Hesper can record which hunks were actually on screen, and for how long, before approval, then warn on approvals given with low coverage of high-risk hunks. Because the aim is to protect the user's own judgment, the warning should be personal and private, not a team metric. Code review anxiety already drives procrastination and rubber-stamping ([overview](https://www.awesomecodereviews.com/research/code-review-research-overview/)).

**Review that preserves comprehension.** The student study found comprehension loss under low-effort agent use. That points to an optional "explain it back" or walkthrough mode for core-logic changes. Practitioners in the JetBrains/Lund study rated chunk-level review highest (**3.91/5**), asked for free navigation instead of a forced tour, wanted live test coverage beside each verdict, and were most skeptical of opaque "security cage" constructs ([arXiv 2606.01969](https://arxiv.org/pdf/2606.01969)).

Some honest uncertainties remain. Every claimed benefit of summaries, walkthroughs and risk UIs for *detection* is unproven. The best evidence is design-study preference and observational merge data. The industry's AI-reviewer metrics are vendor-run and not comparable. Several 2026 preprints in the notes, including the ordering studies and the assertion-judging study, were not fully verified. Hesper should therefore ship enrichments behind measurement. The useful measures are false-positive comments, reverted-after-accept hunks, time to decision and coverage at approval. Hesper can collect them locally because it sees the whole loop from prompt to merge.

## Conclusion

The bottleneck has moved from writing code to deciding whether to trust it. Diff viewers optimize for reading, but the evidence says reading is where bias enters: through order, complexity, AI labels and AI highlights. A review layer earns its place by making the decision honest. It should show which parts of the claim the evidence supports, what was never looked at, and what changed that nobody asked for. Hesper can do this better than PR-side tools because it holds the prompt, the transcript, the observed test runs and the checkpoints in one place, before any of it is flattened into a PR. That makes intent-to-hunk linking and observed rather than narrated evidence achievable natively. Those are the two enrichments no competitor ships.

The practical sequence follows from the strength of the evidence. First build a fast, keyboard-driven, risk-ordered single stream on AppKit with auto-advance and coverage tracking, since these rest on solid evidence and proven engineering. Then add the fleet inbox and checkpoint stacks, which are Hesper's structural advantage. Treat LLM risk scores, walkthroughs and attempt comparison as experiments to measure, not features to trust. If Hesper instruments those experiments, it will be among the first to publish what the field still lacks: how long humans actually take to review agent work, and whether the enrichments make them any more accurate.
