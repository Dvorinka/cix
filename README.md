<h1 align="center">cix</h1>

<p align="center">
  A CI companion CLI for coding agents.<br>
  Push. Wait once. Know exactly what failed.
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

<p align="center">
  <a href="https://github.com/Dvorinka/cix/actions/workflows/ci.yml"><img src="https://github.com/Dvorinka/cix/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/Dvorinka/cix/releases"><img src="https://img.shields.io/github/v/release/Dvorinka/cix" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/Dvorinka/cix" alt="License"></a>
</p>

## What is cix?

cix is a single-binary Go CLI that sits between an agent (or a human) and
GitHub Actions. It exists because the agent↔CI loop is broken in three
places:

1. **Waiting.** Agents poll `gh run watch` / `gh run list` repeatedly,
   burning context and wall-clock on "still running". `cix wait` replaces
   that with one backgroundable process that exits once, with a
   structured result.
2. **Unnecessary CI cycles.** Pushing a branch whose failure was locally
   detectable. `cix preflight` maps changed paths to local equivalent
   checks and runs them before the push leaves the machine.
3. **"It failed before."** Failure memory lives in scrollback nobody
   reads. `cix` stores failure signatures and surfaces prior occurrences
   — `previous_occurrence: run 17321`.

GitHub Actions only, via the `gh` CLI. No daemon, no MCP server, no
LLM summarization, no web UI. One-shot binary, JSONL history, done.

## Features

- **`cix wait`** — resolves the run by `head_sha` (tags included), polls
  the jobs API with backoff, exits early on a *gating* failure with the
  failing step's log slice, its signature, and where it failed before.
  Never fetches logs for running jobs.
- **Deploy-path awareness** — parses the workflow's `needs:` graph
  (`string` or list, matrix suffixes stripped). Non-gating failures are
  warnings: `deploy ✓ · mobile ✗ (non-blocking)` exits `1`, not `2`.
- **`cix push`** — `git push`, find the triggered run, optionally
  `--cancel-superseded` (skipped silently when the workflow already has
  a `concurrency:` group — never fights GitHub's own mechanism), then
  wait.
- **`cix preflight`** — `.cix.yml` maps path globs to local commands;
  only checks matching the actual diff run. Fail-fast, `--all` to
  override.
- **`cix history`** — append-only `history.jsonl` per repo gives
  per-job p50/p90 durations, last conclusion, and signature lookup.
  A running job past its p90 gets a `longer than usual` marker.
- **`cix status`** — one-shot structured snapshot for agents that poll
  themselves anyway.
- **Verify hook** — `.cix.yml` `verify:` runs after a green run, because
  CI green is not the same as deployed-and-healthy.
- **Release artifact gate** — a green `mobile` job is not proof of a
  production build. Checks the workflow ran `bundleRelease` (not
  `assembleDebug`), `app.json` isn't pointing at localhost, forbidden
  dev deps are absent, and `--artifact` files aren't debug-signed.
- **Best-effort notifications** — `--notify` tries `notify-send`, then
  `osascript`.

## Quick Start

Requires the [`gh` CLI](https://cli.github.com/) authenticated to the
repo. Go 1.23+.

```bash
# one-liner — latest release binary to ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/Dvorinka/cix/main/install.sh | sh

# or from source
go install github.com/Dvorinka/cix/cmd/cix@latest
```

```bash
cix push                  # git push, find the run, wait for it
cix wait                  # wait on newest run for HEAD
cix wait --job deploy     # wait on one gate job only
cix status --ref main     # one-shot snapshot
cix preflight             # run checks matching the pending diff
cix history --failures    # what failed before
```

## Commands

### `cix wait [run-id | --ref <ref>]`

The reason the tool exists. Run resolution is `head_sha` first (the
reliable matcher for both branch and tag pushes), with ref-name fallback
for `refs/tags/*`.

- `--pr N` resolves the run for a pull request's head commit — for
  agents that open the PR then watch its checks.
- Any job on the deploy path fails → don't wait for siblings: fetch the
  completed job's log, slice the error window, name the failed step from
  `steps[]`, print, exit `2`.
- All jobs complete → per-job table + captured job outputs, exit `0`.
- Non-gating failure + green deploy path → mixed verdict, exit `1`.
- `--timeout` exceeded → exit `4`.

Flags: `--job <name>` · `--timeout 45m` · `--json` · `--rerun-flaky`
(one-shot retry on transient signatures — runner loss, 5xx, network
timeouts; compile and test failures are never transient) · `--notify` ·
`--artifact <path>` · `--gate <job>`.

### `cix push [--tag <v>] [--cancel-superseded] [wait flags]`

Pushes, finds the triggered run (head_sha — works for tags), optionally
cancels earlier in-progress runs for the same sha, then waits.

### `cix status [--ref <ref>] [--run <id>] [--json]`

Non-blocking snapshot: per-job status, elapsed time, p90 overage markers.

### `cix preflight [--base <ref>] [--files a,b] [--all] [--json]`

Diffs `HEAD` against the base (default `origin/HEAD`), runs only the
checks whose path globs match. A failed required check exits `2` —
the push never happens.

### `cix history [--job <name>] [--failures] [--json]`

Local failure/duration memory from `history.jsonl`.

`cix version` prints the binary version.
`cix completion <bash|zsh|fish>` prints a completion script.

## Configuration

`.cix.yml` at the repo root — all sections optional:

```yaml
checks:
  - name: mobile
    paths: ["apps/mobile/**"]
    run: [cd apps/mobile && npm run typecheck]
  - name: api
    paths: ["apps/api/**", "go.mod"]
    run: [go test ./internal/...]
  - name: docs
    paths: ["**.md"]
    run: [markdownlint-cli2 "**/*.md"]
    optional: true          # warn, don't block

# post-run hook after a green run — exit 3 on failure
verify: "curl -sf https://app.example.com/api/health"

# release artifact gate — runs after these jobs succeed
artifact_jobs: ["mobile"]
artifact:
  api_url_must_not_match: ["localhost", "127.0.0.1", "10.0.2.2"]
  forbid_deps: ["expo-dev-client"]
  require_gradle_tasks: ["bundleRelease"]
  reject_gradle_tasks: ["assembleDebug", "bundleDebug"]

gate: deploy        # deploy-path gate override

# custom failure signatures — matched before the built-in classifiers.
# project-specific flakes get their own name instead of generic/*
signatures:
  - match: "OutOfMemoryError.*Metaspace"
    name: gradle/metaspace-oom
  - match: "Metro has encountered an error"
    name: metro/crash
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | non-gating job failures (deploy path green) |
| `2` | gating job failure, or preflight blocked |
| `3` | post-CI verify or artifact gate failed |
| `4` | timeout |
| `5` | operational error (gh, git, config) |

## JSON output

`cix wait --json` on a gating failure:

```json
{
  "run_id": 17321,
  "conclusion": "failure",
  "failed_job": "mobile",
  "failed_step": "bundle",
  "gating": true,
  "log_slice": "Execution failed for task ':app:createBundleReleaseJsAndAssets'.\nError: Cannot find module 'react-native/rn-get-polyfills'",
  "signature": "metro/module-not-found",
  "previous_occurrence": {"run_id": 17180, "head_sha": "9f3a1c2", "ts": "..."},
  "jobs": [
    {"name": "mobile", "conclusion": "failure", "duration_seconds": 842,
     "gating": true, "failed_step": "bundle", "log_slice": "..."}
  ]
}
```

History lives at `${XDG_DATA_HOME:-~/.local/share}/cix/history.jsonl` —
append-only JSONL, one record per observed job outcome. No sqlite, no
external service.

## Non-goals

- No daemon, no MCP server, no web UI, no LLM log summarization.
- GitHub Actions only — no GitLab/Gitea in v1.
- No CI duration prediction beyond historical p90 reporting.
- `cix` does not run logs through a model; it slices them.

## Development

```bash
go build ./...
go test ./...      # fixture workflows + logs under testdata/
go vet ./...
gofmt -l .
```

Stdlib first; the only dependency is `gopkg.in/yaml.v3`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE)
