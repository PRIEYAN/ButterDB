#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'usage: worktree-remove.sh NN slug lane\n' >&2
	printf 'removes <worktree root>/NN-lane and deletes phase/NN-slug-lane only when every commit is on the merge target\n' >&2
	printf 'environment: BUTTERDB_REPO, BUTTERDB_WT_ROOT, BUTTERDB_MERGE_TARGET (default main)\n' >&2
	printf 'exit codes: 0 removed, 1 failure, 2 usage, 3 worktree removed but branch kept\n' >&2
	exit 2
}

[ $# -eq 3 ] || usage

phase="$1"
slug="$2"
lane="$3"

case "$phase" in
[0-9][0-9]) ;;
*)
	printf 'worktree-remove: phase must be two digits, got "%s"\n' "$phase" >&2
	exit 2
	;;
esac
case "$slug" in
'' | *[!a-z0-9-]* | -* | *-)
	printf 'worktree-remove: slug must be lower case letters, digits and hyphens, got "%s"\n' "$slug" >&2
	exit 2
	;;
esac
case "$lane" in
l[0-9] | l[0-9]-r[0-9] | int | int-r[0-9]) ;;
*)
	printf 'worktree-remove: lane must look like l1, l4-r1 or int, got "%s"\n' "$lane" >&2
	exit 2
	;;
esac

if [ -n "${BUTTERDB_REPO:-}" ]; then
	repo="$(cd "$BUTTERDB_REPO" && pwd -P)"
else
	common_dir="$(git rev-parse --git-common-dir)"
	repo="$(cd "$common_dir/.." && pwd -P)"
fi

wt_root="${BUTTERDB_WT_ROOT:-$repo/../butterdb-wt}"
target="${BUTTERDB_MERGE_TARGET:-main}"
branch="phase/$phase-$slug-$lane"
path="$wt_root/$phase-$lane"

if ! git -C "$repo" show-ref --verify --quiet "refs/heads/$target"; then
	printf 'worktree-remove: merge target branch %s does not exist\n' "$target" >&2
	exit 1
fi

if [ -d "$path" ]; then
	path="$(cd "$path" && pwd -P)"
	if ! git -C "$repo" worktree remove "$path"; then
		printf 'worktree-remove: could not remove %s, it may hold uncommitted work\n' "$path" >&2
		exit 1
	fi
	printf 'worktree removed: %s\n' "$path"
else
	printf 'worktree-remove: no worktree at %s\n' "$path"
fi

if ! git -C "$repo" show-ref --verify --quiet "refs/heads/$branch"; then
	printf 'worktree-remove: branch %s does not exist\n' "$branch"
	exit 0
fi

unmerged="$(git -C "$repo" cherry "$target" "$branch" | grep '^+' || true)"
if [ -n "$unmerged" ]; then
	printf 'worktree-remove: kept branch %s, commits not on %s:\n%s\n' "$branch" "$target" "$unmerged" >&2
	exit 3
fi

git -C "$repo" branch -D "$branch" >/dev/null
printf 'branch deleted: %s\n' "$branch"
