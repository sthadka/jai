// Separate module: proves the jai engine is embeddable via github.com/sthadka/jai/pkg/...
// using only exported APIs — no cobra, TUI, or internal/CLI packages.
module github.com/sthadka/jai/examples/embed

go 1.25.5

require github.com/sthadka/jai v0.0.0

require (
	github.com/mattn/go-sqlite3 v1.14.34 // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace github.com/sthadka/jai => ../..
