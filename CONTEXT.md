# sitrep

A read-only terminal situation report on the work you delegated: one screen showing an epic's tickets, their status, and the code moving them — across GitHub, Jira, and GitLab.

## Language

**Epic**:
A parent ticket whose children are the unit of delegated work being monitored.
_Avoid_: milestone, initiative, parent issue

**Ticket**:
A single work item belonging to an epic.
_Avoid_: issue, story, task, work item (those are tracker-native words; sitrep says Ticket)

**Provider**:
A driver that translates one tracker's API into sitrep's model.
_Avoid_: backend, integration, adapter, platform

**Tracker**:
The external system a Provider talks to (GitHub, Jira, GitLab).

**Status Category**:
Sitrep's normalized lifecycle bucket for a ticket: Todo, InProgress, Done, or Cancelled.

**Native Status**:
The tracker's own status label ("In Review", "Selected for Development"), displayed as-is, never filtered on.

**Capability**:
A per-Provider flag declaring which optional features its tracker supports (hierarchy, blocking links, comments, pull requests).

**Link**:
A directed relationship from one ticket to another (BlockedBy, Blocks, Relates), carrying the tracker's native label for display.

**Profile**:
A named entry in the user's global config binding a tracker host, project, and auth reference.
_Avoid_: context (clashes with terminal/Go usage), account

**Epic Ref**:
The user-supplied pointer to an epic: a bare number (resolved via the cwd's git remote), a Jira-style key (resolved via Profile prefix match), or a full URL.

**Detail**:
The expensive, per-ticket data (description, comments, links) fetched only when a ticket is opened — never during list rendering.

## Relationships

- An **Epic** has many **Tickets**; a **Ticket** has at most one parent (tree-shaped data, rendered flat in v1)
- A **Provider** serves one **Tracker** and declares its **Capabilities**
- A **Ticket** carries a **Status Category** plus a **Native Status**; filtering and progress counts use only the Category
- A **Ticket**'s **Links** and comments live in its **Detail**, not in the list model
- A **Profile** selects a **Provider** and supplies what it needs to connect

## Example dialogue

> **Dev:** "Does the list view show a **Ticket**'s comments?"
> **Domain expert:** "No — comments are **Detail**, fetched only when you open the ticket. The list call stays one cheap batched request per **Epic**."
>
> **Dev:** "Can I filter on 'In Review'?"
> **Domain expert:** "You filter on the **Status Category** (InProgress); 'In Review' is a **Native Status** — display only, because every Jira instance invents its own."

## Flagged ambiguities

- "epic" means different things per tracker (GitHub: issue with sub-issues; Jira: epic issue type via `parent`; GitLab: Premium epic, or milestone-as-epic fallback on Free). In sitrep, **Epic** always means the normalized parent — the Provider hides the tracker's flavor.
- sitrep is **read-only by design** — it never writes to a tracker. Agents write; sitrep watches.
