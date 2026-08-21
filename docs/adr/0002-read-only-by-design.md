# sitrep is read-only by design

sitrep never writes to a tracker: no transitions, no comments, no assignment. In this workflow agents perform the writes and sitrep is the human's monitor; a write surface would duplicate what `gh`/`glab`/agents already do and drag in a much larger auth/permission story. Nothing in the design should actively preclude a far-future status-transition action, but no code anticipates it.
