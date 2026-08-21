# Go with Bubble Tea for the TUI

The author is a Node.js developer, so Go is the surprising choice — but sitrep's home is headless SSH boxes, and a static single binary with no runtime beats Ink (needs Node, ~30 FPS render throttle) and OpenTUI (too young, Bun FFI). Bubble Tea v2 + Bubbles v2 supply the list (fuzzy filter, pagination), viewport, and help components the app is made of; Ratatui (Rust) offered no component library of that maturity and a steeper event-loop cost. All Go-specific decisions are delegated to the implementing agent; the author reviews architecture, not idiom.
