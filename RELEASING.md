# Releasing accountant

A release is two semver git tags on the same `main` commit: `vX.Y.Z` and `metrics/vX.Y.Z`. The Go module proxy
serves the tags. There is no binary to build.
Pushing a `v*` tag runs [.github/workflows/release.yaml](.github/workflows/release.yaml).

## Why two tags

The repo has two Go modules:

- `github.com/rossoctl/accountant` (the root, stdlib only).
- `github.com/rossoctl/accountant/metrics` (in `metrics/`, imports OpenTelemetry).

Go finds a nested module only from a tag that starts with its folder name: `metrics/vX.Y.Z`. A tag `vX.Y.Z`
does not version `metrics`. Without the second tag, a host must pin `metrics` to a commit pseudo-version.
Hosts pin tags, never commits. So each release has both tags, with the same version number.

`metrics/go.mod` requires `github.com/rossoctl/accountant v0.0.0` and uses `replace ... => ../`. Go ignores a
`replace` in a dependency. A host must require both modules at the same version. The host's own `require`
then wins (MVS picks the highest version), and `v0.0.0` is never fetched.

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
3. Create both tags on the same commit:
   ```
   git tag -s vX.Y.Z -m "vX.Y.Z"
   git tag -s metrics/vX.Y.Z -m "metrics/vX.Y.Z"
   ```
4. Push both tags in one command: `git push origin vX.Y.Z metrics/vX.Y.Z`.
   Do not push `metrics/vX.Y.Z` alone. Only a `v*` tag starts the workflow, so a lone metrics tag is not checked.
5. Wait for the Release workflow. It does these things:
   - It fails if the tag is not semver or the commit is not on `main`.
   - It fails if `metrics/vX.Y.Z` is missing, does not point to a commit, or is on another commit.
     - Missing: the message names the commands that create and push the tag.
     - Not a commit: the Go module proxy cannot serve such a tag. The message names the commands that delete it and create it again on the release commit.
     - On another commit: the message says not to move a published tag. Publish the next PATCH version.
     After you fix a missing tag or a tag that is not a commit, re-run the workflow: Actions, the failed run, Re-run. A push of the metrics tag does not start a run.
   - It runs the same checks as `ci.yaml`, for the root module and for the nested `metrics` module.
   - It asks the Go module proxy for both tags before it creates the release. A tag that Go rejects as a version fails here, and no release is made.
   - It creates the GitHub Release with generated notes. A tag with a `-suffix` is a pre-release.
6. Bump each host. Bump both modules to the same version:
   `go get github.com/rossoctl/accountant@vX.Y.Z github.com/rossoctl/accountant/metrics@vX.Y.Z && go mod tidy`.
   The hosts are context-guru and Cortex.

## If a release is wrong

Do not move or delete a published tag. This holds for `vX.Y.Z` and for `metrics/vX.Y.Z`.
The Go module proxy caches each tag for good.
Fix the problem and publish the next PATCH version. If the bad version must not be used,
add a `retract` directive for it to `go.mod`. Do this in `metrics/go.mod` too when `metrics/vX.Y.Z` is bad.

## History

- `metrics/v0.8.0` did not come from the workflow. A maintainer created it by hand on 2026-11-25, after the fact,
  on commit 52dd9e6 (the commit of `v0.8.0`). Before it, hosts pinned `metrics` to a commit pseudo-version.
  From the next release, both tags are part of the process.
