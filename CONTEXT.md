# sitrep

A read-only terminal ticket viewer: one Watchlist showing Tickets, their status, and the code
moving them across GitHub, Jira, and GitLab. Agents write; sitrep watches.

## Language

**Watchlist**:
The set of Tickets shown on one list screen or in one one-shot report. A Watchlist is resolved
anew on every refresh and belongs to one Provider and Tracker connection, with at most one
selected Profile.

**Selector**:
A closed instruction naming a Watchlist: one Epic Ref, an exact Ref list, or an opaque Query.
Refs read from stdin are transport for the exact Ref-list Selector, not another Selector kind.

**Ref**:
A user-supplied pointer to one Ticket or Epic: a bare number (resolved through the current
clone's git origin), a Jira key (resolved by Profile prefix), a Tracker-native Ref, or a full
URL.

**Query**:
An opaque tracker-native membership expression supplied with `--query`. sitrep passes it to
the selected Tracker exactly; it neither parses nor rewrites the Query.

**Epic**:
A parent Ticket whose children can form a Watchlist through the Epic Selector. An Epic is one
way into sitrep, not the boundary of every Watchlist.
_Avoid_: milestone, initiative, parent issue

**Ticket**:
A single work item. It may be an Epic's child, an exact Ref-list member, a Query result, or a
Ticket opened directly; it need not belong to an Epic.
_Avoid_: issue, story, task, work item (those are tracker-native words; sitrep says Ticket)

**Provider**:
A driver that translates one Tracker's API into sitrep's model.
_Avoid_: backend, integration, adapter, platform

**Tracker**:
The external system a Provider talks to (GitHub, Jira, GitLab).

**Status Category**:
sitrep's normalized lifecycle bucket for a Ticket: Todo, InProgress, Done, or Cancelled.

**Native Status**:
The Tracker's own status label ("In Review", "Selected for Development"), displayed as-is
and never filtered on. A Native Status that only restates its Status Category ("open" under
Todo, "closed" or "Done" under Done) carries no information, so renderers suppress it wherever
the Status Category is already supplied — list rows and epic-report rows under their Category
heading, and Frontier cards, whose colour carries the Category. Where nothing else supplies
the context the Status Category is drawn in its place, so a Detail header and a single-Ticket
report always name a status; a Detail LINKS row keeps the Tracker's own word, which is the
only status signal it has. The word itself is never rewritten and
never branched on for meaning, and `--json` always carries the Tracker's own word.

**Capability**:
A per-Provider declaration of supported Selector kinds and optional data features such as
hierarchy, blocking links, comments, and pull requests. Missing optional data Capabilities
are silent; choosing an unsupported Selector fails loudly.

**Rate Limit Budget**:
What a Provider can report about the request allowance it has left with its Tracker, and when
that allowance resets. Trackers meter differently and some cannot report one at all, so a Rate
Limit Budget is an optional Capability: when it is absent sitrep says nothing rather than
inventing a number. Spending the allowance well — batching reads, backing off after a refusal,
not polling a screen nobody is watching — is required of every Provider whether or not it can
report one.

**Link**:
A directed relationship from one Ticket to another (BlockedBy, Blocks, Relates), carrying
the Tracker's Native label for display.

**Profile**:
A named entry in the user's global config selecting a Provider and supplying any needed
Tracker host, project, and auth references.
_Avoid_: context (clashes with terminal/Go usage), account

**Detail**:
The expensive, per-Ticket data (description, comments, links) fetched only when a Ticket is
opened — never during list rendering.

**Trail**:
The ordered, session-local path of Tickets followed through explicit Links in Detail.
Following a Link pushes the current Ticket; Esc returns to the preceding Ticket.

**Frontier**:
The screen that renders a Watchlist as nodes and BlockedBy/Blocks edges to answer one
question: which Tickets can be picked up right now. It is a second rendering of the same
Watchlist, not another Selector kind.
_Avoid_: graph view, DAG, dependency tree, node view

**Actionable**:
A derived property of a Ticket: its Status Category is Todo and every Ticket it is BlockedBy
has Status Category Done or Cancelled. A blocker whose status could not be read leaves the
Ticket not Actionable — the property fails closed, because telling an agent to start work on
a Ticket whose blockers were never verified is the one wrong answer that costs something.
_Avoid_: ready, unblocked, available

**Ghost Ticket**:
A Ticket that appears on the Frontier only as the target of a Link from a Watchlist member,
without being a member itself. It is drawn so that a Ticket blocked from outside the
Watchlist never looks Actionable. Its Links are not followed to discover further Tickets.

## Relationships

- A **Selector** resolves one **Watchlist**; every refresh resolves the same Selector again.
- A **Watchlist** has many **Tickets** and uses one **Provider**, Tracker connection,
  credential scope, Capability set, and staleness clock, with at most one selected Profile.
- An **Epic** has many child **Tickets**; a Ticket has at most one parent (tree-shaped data,
  rendered flat in the current list).
- A **Ticket** carries a **Status Category** plus a **Native Status**; filtering and progress
  counts use only the Category.
- A Query search chooses stable member identities; authoritative exact reads supply the thin
  Ticket state that is rendered.
- A Ticket's **Links** and comments live in its **Detail**, not in the list model.
- Following an explicit **Link** appends to the **Trail**; Esc removes one Trail step and
  restores the preceding Ticket.
- A **Profile** selects a **Provider** and supplies what it needs to connect.
- The **Frontier** renders one **Watchlist**; membership is the Watchlist's, plus **Ghost
  Tickets** pulled in as Link targets. It never changes which Tickets the Watchlist contains.
- **Actionable** is computed from a Ticket's Status Category and its BlockedBy **Links**;
  because Links live in **Detail**, the Frontier fetches Detail per Ticket and the list
  refresh never does.
- The list may *display* Actionable when the session's Detail cache is already warm for the
  whole **Watchlist**, and shows nothing at all otherwise — never a marker on the cached
  subset, and never a fetch to find out.
- A cycle of BlockedBy Links makes every Ticket in it permanently not Actionable; the
  Frontier reports the cycle rather than hiding it.

## Example dialogue

> **Dev:** "Does `Resolve(Selector)` return a Ticket's comments?"
> **Domain expert:** "No — Resolve returns a thin Watchlist reading. Comments are Detail,
> fetched only through `FetchDetail` when the Ticket is opened."
>
> **Dev:** "Can I filter on 'In Review'?"
> **Domain expert:** "You filter on the Status Category (InProgress); 'In Review' is a Native
> Status — display only, because every Jira instance invents its own."
>
> **Dev:** "Does a Query result's title come from search?"
> **Domain expert:** "No. Search chooses membership, then the Provider rereads every selected
> identity authoritatively before it renders a Watchlist."

## Flagged ambiguities

- "epic" means different things per Tracker (GitHub: issue with sub-issues; Jira: Epic issue
  type via `parent`; GitLab: Premium/Ultimate native Epic, or milestone-as-Epic fallback on
  Free). In sitrep, **Epic** always means the normalized parent; the Provider hides the
  Tracker's flavor.
- Query membership reflects the Tracker search that ran during this Resolve, while rendered
  Ticket fields come from subsequent authoritative reads. Membership can change between
  refreshes and a Query cutoff is not a known total.
- Refs in one Watchlist may cross repositories or projects only when one Provider and Tracker
  connection can serve all of them. If routing selects a Profile, every Ref must use it;
  mixed-Tracker or mixed-connection Watchlists fail rather than merging credential and
  Capability boundaries.
- An OSC 8 terminal hyperlink may open a URL through the terminal, while following an
  explicit Link inside sitrep extends the Trail. These are separate actions.
- **Actionable** is sitrep's own computation, not a Tracker field. It is orthogonal to Status
  Category: an InProgress Ticket can be blocked, and that combination is a signal to show,
  not a contradiction to resolve. Colour on the Frontier carries Status Category; Actionable
  and blocked are carried by a separate visual channel.
- Filtering hides rows in a list but would delete edges on the **Frontier**, which can make a
  blocked Ticket look Actionable. The Frontier therefore renders the complete Watchlist.
- sitrep is **read-only by design** — it never writes to a Tracker. Agents write; sitrep
  watches.
