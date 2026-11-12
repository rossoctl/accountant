# Releasing accountant

A release is a semver git tag on `main`. The Go module proxy serves the tag. There is no binary to build.
Pushing a `v*` tag runs [.github/workflows/release.yaml](.github/workflows/release.yaml).

## Choose the version

While the version is below 1.0, use these rules:

- MINOR (`v0.2.0`): a change that alters `Compute` results, or a change to the public API in
  `accountant.go`, or a change to the generated cases in `test/cases/`. Hosts import these cases,
  so a case change can break a host test.
- PATCH (`v0.1.2`): a fix that changes none of the above.

Say in the PR title which kind a change is, so the release notes show it.

## Steps

1. Merge the work to `main`. Wait for CI to pass on `main`.
2. Fetch and check out `main`: `git fetch origin main && git checkout origin/main`.
3. Create the tag: `git tag -s vX.Y.Z -m "vX.Y.Z"`.
4. Push the tag: `git push origin vX.Y.Z`.
5. Wait for the Release workflow. It does these things:
   - It fails if the tag is not semver or the commit is not on `main`.
   - It runs the same checks as `ci.yaml`.
   - It creates the GitHub Release with generated notes. A tag with a `-suffix` is a pre-release.
   - It asks the Go module proxy for the tag, so hosts can resolve it at once.
6. Bump each host: `go get github.com/rossoctl/accountant@vX.Y.Z && go mod tidy`.
   The hosts are context-guru and Cortex.

## If a release is wrong

Do not move or delete a published tag. The Go module proxy caches it for good.
Fix the problem and publish the next PATCH version. If the bad version must not be used,
add a `retract` directive for it to `go.mod`.
