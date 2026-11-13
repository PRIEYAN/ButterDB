#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
chmod +x "$repo_root/scripts/hooks/commit-msg" "$repo_root/scripts/hooks/pre-push"
git -C "$repo_root" config core.hooksPath scripts/hooks
printf 'butterdb: hooks installed from scripts/hooks\n'
