# Loops: keep track of what you started

Agents finish turns all day. Each one leaves something behind: a PR to
merge, a question for you, a step only you can take, a promise to do more
later. A loop is Hesper's record of one piece of work, so none of that is
lost when you close a tile.

## What a loop is

A loop holds:

- **its agents and sessions**, and who started or continued whom: a lead
  agent, the helpers it started, the session you resumed a day later on
  another Mac;
- **what it links**: projects, PRs and other URLs, files handed from one
  agent to the next, and other loops it waits for or blocks;
- **its items**: steps, loose ends, promises, questions and offers. Each
  item belongs to **you**, to **an agent** or to **others**, and can come
  after other items.

A loop outlives its agents. Closing, tidying or moving an agent never
closes its loop; only you do, as **done** or **dropped**. You can also
**park** a loop until tomorrow or a date, and it leaves your way until
then.

## Who acts next

Every loop says who acts next:

| Next | When |
|---|---|
| **You** | an item of yours is open, one of its agents waits for your approval or answer, or an agent's last turn asked you something or named a step of yours |
| **Agents** | one of its agents is working |
| **Others** | an item waits on someone else, or the loop waits for another open loop |
| **Parked** | you parked it |
| **Closed** | done or dropped |

When nothing is open and nobody works, the loop **looks done**: it is on
you, to close it.

The board (⌘L) has a column for each of the first four. ⌘J's queue ends
with **Your move**: one entry per loop waiting on you, with what to do
(Yes / No for an agent's question, Did it, Copy command, Later). Offers,
such as "Want me to also add tests?", are kept in the loop but never
notify you. The evening wrap-up (⇧⌘E) walks through today's loops once
and suggests done, drop, park or the next step for each.

## How loops form

You do not have to make loops. Hesper does, as agents start:

- a new agent starts a new loop, named after its task;
- an agent started by another agent joins its parent's loop;
- resuming, forking or continuing a session (also as another kind of
  agent), and moving an agent to another Mac, keep it in its loop;
- when an agent's turn ends, Hesper reads its final message: a question
  for you, a step that is yours, or something it waits for becomes an
  item; a closing offer becomes an offer.

You can still start a loop by hand (`hesperctl loops new`), file an agent
into a chosen loop (`hesperctl new --loop`), merge two loops that turned
out to be one, or split an agent and what it started into a loop of its
own.

**Agents use loops too.** Inside Hesper, an agent's own loop is `self`:
it puts follow-ups there (`hesperctl loops item add self --owner you
merge PR 482 once CI is green`), ticks off what it finished and links the
PR. It may change only its own loop's items and links. An agent can ask
to hear about its helpers: started with `--report-to-parent`, each helper
tells it in one line when its turn ends.

## Goals and milestones

A product is a project or a group of projects. It can have **goals**
(outcomes it works toward) and **milestones** (a release or a result).
A milestone has:

- **done when** conditions, which you tick off (`hesperctl goals check`)
  or the Steward checks for you (a PR merged, a tag pushed);
- an optional due date;
- the loops filed under it (`hesperctl goals file`, or start an agent with
  `--milestone`). With **auto-file** on, the Steward files the loops it
  matches without asking.

The product page (⇧⌘L) shows each milestone's progress and where it
stands: done, on you, waiting on another milestone, stalled (nothing
changed for 7 days), on track; and the loops not filed anywhere yet.

A **planning session** (`hesperctl goals plan app`, or from the product
page) is an agent that reads the product's loops and proposes milestones.
Proposed milestones wait for you: accept them all or one by one, or drop
them.

## The Steward

The Steward is a background agent that keeps the loops up to date. It
wakes when a turn ends, an agent closes, a loop starts, a review changes,
and once in the evening, at most 40 times a day.

It is **off until you turn it on**: Settings › Steward, or
`hesperctl steward on`. One of your Macs runs it (`--machine`), and you
choose how much it may do by itself:

| Level | It may |
|---|---|
| **watch** | keep the records: link PRs and files, note endings and loose ends, tick off conditions it can check |
| **suggest** (default) | also save drafts for the next steps, for you to start |
| **act** | also start drafts that are ready, and merge or split loops |

At every level it **never** closes a loop, goal or milestone, never
accepts a plan, never changes its own settings or level, and never
answers an agent's approval or question or types into an agent. It runs
with a fixed set of read-mostly tools and never skips permissions. Its
own work never wakes it again.

## Privacy

Loops, goals, milestones and the Steward's settings stay on your Macs.
Each Mac keeps them in `~/.local/state/hesper/loops.json` (readable only
by you) and shares them with your other Macs over the same end to end
encrypted link as your agents. The relay forwards that traffic without
being able to read it: it sees no loop, title, item, URL or id. Hesper.app
never sends any of it to the network; the Steward's checks run with your
own `gh`.

## From the command line

| Command | What it does |
|---|---|
| `hesperctl loops ls` | open and parked loops with who acts next |
| `hesperctl loops show LOOP` | a loop's items and links |
| `hesperctl loops graph LOOP` | its agents as a tree, with files and blocked loops |
| `hesperctl loops item add\|check\|rm` | its items |
| `hesperctl loops park\|close\|merge\|split` | park, close, merge, split |
| `hesperctl goals ls\|new\|check\|file\|plan\|accept` | goals and milestones |
| `hesperctl steward status\|on\|off\|level` | the Steward |

`hesperctl reference` documents every flag.
