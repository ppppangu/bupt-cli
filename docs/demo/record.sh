#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
mkdir -p docs/demo/assets
if [ "$#" -eq 0 ]; then
    set -- tui-compute tui-model tui-integration cli-demo
fi
for workflow do
    case "$workflow" in
        tui-compute|tui-model|tui-integration|cli-demo) ;;
        *) printf 'Unknown workflow: %s\n' "$workflow" >&2; exit 2 ;;
    esac
    BUPT_DEMO_WORKFLOW="$workflow" go test ./internal/control -run '^TestREADMEWorkflow$' -count=1 -v
done
