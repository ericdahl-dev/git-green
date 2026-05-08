# Go + Bubble Tea for TUI

We're building the TUI in Go using [Bubble Tea](https://github.com/charmbracelet/bubbletea) as the UI framework and `google/go-github` for GitHub API access. Go produces a single distributable binary with no runtime dependency, Bubble Tea is the most mature terminal UI framework in the ecosystem, and the two libraries compose naturally. Alternatives considered: Python/Textual, Rust/Ratatui, Node/Ink — all capable but none offer the same combination of strong GitHub API client, mature TUI primitives, and easy distribution.
