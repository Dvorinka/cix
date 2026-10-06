package internal

import "fmt"

// Completion prints a shell completion script for the given shell.
func Completion(shell string) (string, error) {
	switch shell {
	case "bash":
		return `_cix() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=($(compgen -W "wait push status preflight history version completion" -- "$cur"))
    return 0
  fi
  case "$prev" in
    --run|--pr|--job|--timeout|--base|--tag) COMPREPLY=(); return 0 ;;
    --files|--artifact) COMPREPLY=($(compgen -f -- "$cur")); return 0 ;;
    completion) COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur")); return 0 ;;
  esac
  COMPREPLY=($(compgen -W "--run --ref --pr --job --timeout --rerun-flaky --notify --artifact --tag --cancel-superseded --base --files --all --failures --gate --json" -- "$cur"))
}
complete -F _cix cix
`, nil
	case "zsh":
		return `#compdef cix
_cix() {
  local -a cmds=(wait push status preflight history version completion)
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
    return
  fi
  _arguments \
    '--run[run id]:run:' \
    '--ref[branch or tag]:ref:' \
    '--pr[pull request]:pr:' \
    '--job[single job]:job:' \
    '--timeout[max wait]:timeout:' \
    '--rerun-flaky[rerun transient]' \
    '--notify[desktop notification]' \
    '--artifact[artifact path]:file:_files' \
    '--tag[tag or HEAD]:tag:' \
    '--cancel-superseded[cancel older runs]' \
    '--base[base ref]:ref:' \
    '--files[changed files]:files:' \
    '--all[all checks]' \
    '--failures[failures only]' \
    '--gate[gate job]:job:' \
    '--json[structured output]'
}
_cix "$@"
`, nil
	case "fish":
		return `complete -c cix -n '__fish_use_subcommand' -a 'wait push status preflight history version completion'
complete -c cix -l run -r -d 'run id'
complete -c cix -l ref -r -d 'branch or tag'
complete -c cix -l pr -r -d 'pull request number'
complete -c cix -l job -r -d 'single job'
complete -c cix -l timeout -r -d 'max wait'
complete -c cix -l rerun-flaky -d 'rerun transient failure'
complete -c cix -l notify -d 'desktop notification'
complete -c cix -l artifact -r -F -d 'artifact path'
complete -c cix -l tag -r -d 'tag or HEAD'
complete -c cix -l cancel-superseded -d 'cancel older runs'
complete -c cix -l base -r -d 'base ref'
complete -c cix -l files -r -d 'changed files'
complete -c cix -l all -d 'all checks'
complete -c cix -l failures -d 'failures only'
complete -c cix -l gate -r -d 'gate job'
complete -c cix -l json -d 'structured output'
`, nil
	}
	return "", fmt.Errorf("unknown shell %q — use bash, zsh, or fish", shell)
}
