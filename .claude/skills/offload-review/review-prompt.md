# Review an accountant pull request

You review one pull request in the `accountant` repo. Another reviewer reviews the same PR
at the same time.

- **Round 1:** review on your own. Do not read other reviews or comments on the PR until you
  post your own. Two independent first views find more defects than one.
- **Later rounds:** read the whole PR conversation first, as "This review" says.

## Be thorough

Your review is the team's check before merge. It is essential that you read **every changed
line, every new commit, and every comment** that the steps below name. Read each one in full.
A review that skips any of them is a **failed review**, even when its verdict is right. If you
cannot read something, say so in the review and use `Verdict: blocking`.

## What to do

1. Read `CLAUDE.md`, `docs/spec.md` and `docs/design.md` for the rules of this repo.
2. Read the full diff and the code around each change.
3. For each finding you believe, try to prove it: exact lines, a reasoning trace, or a test.
4. Write your review to `.review.md` in your folder, in the format below. "This review" gives
   the exact path.
5. Post it on the PR with the command given under "This review". Post it only one time.

## Do not

- Do not change, commit, or push code. Your only file is `.review.md`.
- Do not approve or request changes through GitHub. Post a comment review only.
- You may run `go vet ./...` and `go test ./...` in your folder. They are local and write nothing
  outside it. Do not run anything else that changes files or reaches the network.

## Rules

- **The module imports only the standard library** (R3). The public API is `accountant.go` only.
  The docs in `docs/` must describe the code: a change to `Compute` without a doc change is a defect.
- Report real defects: wrong pricing, a wrong cache effect, a check that misses an error, a
  missing test for new behavior, a doc that disagrees with the code.
- Leave out style nitpicks. If a small point is worth noting, put it under "Non-blocking".
- Put all real findings in this round. Rounds are limited.

## Format of `.review.md`

Write in ASD-STE100 style: short sentences, simple words, active voice. Keep the first two
lines exactly as "This review" gives them: the marker and the identity table.

```
<marker line>
<identity table>

Verdict: approve | blocking

## Findings
1. **<file>:<line> — <one-line claim>**
   - What fails: <inputs or state → wrong result>
   - Proof: <lines, trace, or test and its output>
   - Fix: <short proposal>

## Non-blocking
- ...

## What I checked
- ...
```

Use `Verdict: blocking` when there is at least one finding. Use `Verdict: approve` when there
is none. Put the same verdict in the marker line and the table.
