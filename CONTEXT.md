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
and never filtered on.

**Capability**:
A per-Provider declaration of supported Selector kinds and optional data features such as
hierarchy, blocking links, comments, and pull requests. Missing optional data Capabilities
are silent; choosing an unsupported Selector fails loudly.

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
- sitrep is **read-only by design** — it never writes to a Tracker. Agents write; sitrep
  watches.
