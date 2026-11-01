# CLAUDE.md - cost

`cost` is the context optimization saving tracker: a stdlib-only Go library that prices context
operations in tokens per billing tier. See [README.md](README.md) and [docs/](docs/).

## Rules

- Go 1.26, module `github.com/rossoctl/cost`, package `cost`. **The module imports only the standard
  library** (R3). `boundary_test.go` fails on any other import. Hosts (context-guru, Cortex) import
  `cost`, never the reverse. Do not add host code, host names or host paths to Go files.
- The public API is `cost.go` only: type aliases for `internal/engine`. A new exported name in the engine
  must be added there, or listed as hidden in `api_test.go`, or that test fails. Hosts import `cost`
  and `cost/test/cases`, never `internal/engine`.
- `Compute` is deterministic and does no I/O (R26). The one exception is a `log/slog` warning for each
  failed check.
- Cases in `test/cases/*.json` are generated. Edit `test/gen/gen_cases.py`, then run
  `go generate ./test`. Never edit a JSON case by hand.

## Development procedure

1. Work on a branch. Do not commit to `main`.
2. Before a push, run these from the repo root. All three must be clean:
   - `gofmt -l .` (it must print nothing; always give it a path)
   - `go vet ./...`
   - `go test ./...`
3. If you changed a dependency or removed an import, run `go mod tidy -diff`. CI gates on it.
4. If you changed `test/gen/gen_cases.py`, run `go generate ./test`. CI fails when the JSON cases are
   out of date.
5. If you change the behavior of `Compute`, update `docs/` in the same PR. The spec and the design
   are the source of truth. A code change that the docs do not describe is a bug.
6. **Commits: DCO sign-off is mandatory** (`git commit -s`). Author as the repo owner. AI attribution
   uses `Assisted-By: Claude (Anthropic AI) <noreply@anthropic.com>`; never `Co-Authored-By` or a
   "Generated with" line. Conventional-commit titles (`feat:`, `fix:`, `refactor:`, `docs:`).
7. Open a PR. End the PR body with the same `Assisted-By` line.

## Review process

Every PR gets two independent reviews before it is offered for merge. Do not wait for the user to ask.

- Fix each finding, or reply on the PR with the reason and proof. Reply to every finding.
- The budget is 2 rounds. Only the user grants more; you may ask.
- Run the `offload-review` skill (`.claude/skills/offload-review`, script `scripts/offload-review.sh`).
  Run it in the main session, not in a subagent. It starts two reviewers, and each posts a review on
  the PR. After the PR merges or closes, run `scripts/offload-review.sh <pr> --cleanup`.
- On macOS, set `OFFLOAD_CLAUDE_MODEL` if the reviewer backend must use a given model. Set
  `OFFLOAD_REVIEW_SCOPE` to limit what the reviewers read (for example, to skip generated JSON cases).

## How to write for me: ASD-STE100 style

Applies to every message the user must read: chat replies, issues, PRs, PR reviews, commit messages,
docs.

Write in the style of ASD-STE100 (Simplified Technical English). Less prose, less jargon.

- **Short sentences.** One idea per sentence. About 20 words at most in an instruction, 25 in a
  description.
- **Simple, common words.** Use one word for one meaning, and keep it the same all the way through.
  Prefer "use" to "leverage", "start" to "initiate", "help" to "facilitate".
- **Active voice.** Say who does what: "The sweep removes 2 outputs", not "2 outputs are removed".
- **No jargon without need.** If a technical term is necessary, say what it means the first time.
- **Lead with the result.** Then the facts, as numbered or bulleted points with real numbers.
- **Lists over paragraphs.** Bold the key fact in a point when it helps a fast reader.
- **No filler.** No hedging, no restating the question, no closing recap.

**Issues and PRs especially.** The description must be easy to read for someone who did not do the
work. Use short sections such as *Summary*, *What we found*, *Why it happens*, *Proposal* (or
*What changed* / *How it was tested* for a PR), *Open questions*. Put deeper technical detail in a
separate *Technical notes* section at the end.
