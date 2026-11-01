# CLAUDE.md - cost

`cost` is the context optimization saving tracker: a stdlib-only Go library that prices context
operations in tokens per billing tier. See [README.md](README.md) and [docs/](docs/).

## Rules

- Go 1.26, module `github.com/rossoctl/cost`, package `cost`. **The module imports only the standard
  library** (R3). `boundary_test.go` fails on any other import. Hosts (context-guru, Cortex) import
  `cost`, never the reverse. Do not add host code, host names or host paths to Go files.
- `Compute` is deterministic and does no I/O (R26). The one exception is a `log/slog` warning for each
  failed check.
- Cases in `test/cases/*.json` are generated. Edit `test/gen/gen_cases.py`, then run
  `go generate ./test`. Never edit a JSON case by hand.
- The word "counterfactual" is the idea (the bill without the change), not the name of the package.
- Before a push: `gofmt -l .`, `go vet ./...`, `go test ./...`.
- **Commits: DCO sign-off is mandatory** (`git commit -s`). AI attribution uses `Assisted-By:`; never
  `Co-Authored-By` or a "Generated with" line. Conventional-commit titles.
