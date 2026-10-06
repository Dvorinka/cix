# Contributing

Contributions welcome. The bar is a green pre-push gate and a test for any
non-trivial logic.

## Workflow

1. Fork, branch from `main`.
2. Make the change. Keep it small — one concern per PR.
3. Run the gate:

   ```bash
   gofmt -l .
   go vet ./...
   go test ./...
   go build ./...
   ```

   (or `cix preflight` — the repo's own `.cix.yml` does exactly this)

4. Open a PR describing *why*, not just *what*.

## Conventions

- Single Go module, standard library first; `gh` CLI is the only GitHub
  transport. New dependencies need justification in the PR.
- The GitHub API layer (`internal/gh.go`) is an interface — tests use
  fakes, never live API calls.
- JSON output is a contract: stable snake_case keys.
- Exit codes are part of the contract — `0` ok, `1` non-gating warnings,
  `2` gating failure/blocked, `3` verify/artifact gate, `4` timeout,
  `5` operational error. Don't reuse them loosely.
- Fixtures (workflows, job logs) live in `testdata/`.

## Reporting bugs

Include the repo's workflow file shape (needs/matrix/jobs), the `cix`
command and `--json` output, and the expected vs actual exit behavior.
