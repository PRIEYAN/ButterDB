#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'usage: worktree-create.sh NN slug lane [base]\n' >&2
	printf 'creates <worktree root>/NN-lane on branch phase/NN-slug-lane\n' >&2
	printf 'environment: BUTTERDB_REPO (main checkout), BUTTERDB_WT_ROOT (worktree root)\n' >&2
	exit 2
}

[ $# -ge 3 ] && [ $# -le 4 ] || usage

phase="$1"
slug="$2"
lane="$3"
base_ref="${4:-}"

case "$phase" in
[0-9][0-9]) ;;
*)
	printf 'worktree-create: phase must be two digits, got "%s"\n' "$phase" >&2
	exit 2
	;;
esac
case "$slug" in
'' | *[!a-z0-9-]* | -* | *-)
	printf 'worktree-create: slug must be lower case letters, digits and hyphens, got "%s"\n' "$slug" >&2
	exit 2
	;;
esac
case "$lane" in
l[0-9] | l[0-9]-r[0-9] | int | int-r[0-9]) ;;
*)
	printf 'worktree-create: lane must look like l1, l4-r1 or int, got "%s"\n' "$lane" >&2
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
mkdir -p "$wt_root"
wt_root="$(cd "$wt_root" && pwd -P)"

branch="phase/$phase-$slug-$lane"
path="$wt_root/$phase-$lane"
phase_branch="phase/$phase-$slug"

if [ -z "$base_ref" ]; then
	if git -C "$repo" show-ref --verify --quiet "refs/heads/$phase_branch"; then
		base_ref="$phase_branch"
	else
		base_ref="HEAD"
	fi
fi

base="$(git -C "$repo" rev-parse --verify "$base_ref^{commit}")"

if git -C "$repo" show-ref --verify --quiet "refs/heads/$branch"; then
	printf 'worktree-create: branch %s already exists\n' "$branch" >&2
	exit 1
fi
if [ -e "$path" ]; then
	printf 'worktree-create: %s already exists\n' "$path" >&2
	exit 1
fi

git -C "$repo" worktree add -b "$branch" "$path" "$base" >/dev/null

head="$(git -C "$path" rev-parse HEAD)"
current="$(git -C "$path" branch --show-current)"
dirty="$(git -C "$path" status --porcelain --untracked-files=all)"

if [ "$head" != "$base" ] || [ "$current" != "$branch" ] || [ -n "$dirty" ]; then
	printf 'worktree-create: created worktree failed verification at %s\n' "$path" >&2
	exit 1
fi

printf 'worktree: %s\n' "$path"
printf 'branch: %s\n' "$branch"
printf 'base: %s\n' "$base"
