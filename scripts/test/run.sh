#!/usr/bin/env bash
set -euo pipefail

test_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
scripts_dir="$(cd "$test_dir/.." && pwd -P)"
fixtures="$test_dir/fixtures"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

passed=0
failed=0
last_output=""
last_status=0

run_script() {
	last_status=0
	last_output="$("$@" 2>&1)" || last_status=$?
}

expect_status() {
	[ "$last_status" -eq "$1" ] || {
		printf '    expected exit %s, got %s\n%s\n' "$1" "$last_status" "$last_output" >&2
		return 1
	}
}

expect_output() {
	case "$last_output" in
	*"$1"*) ;;
	*)
		printf '    expected output to contain: %s\n%s\n' "$1" "$last_output" >&2
		return 1
		;;
	esac
}

expect_no_output() {
	case "$last_output" in
	*"$1"*)
		printf '    output must not contain: %s\n%s\n' "$1" "$last_output" >&2
		return 1
		;;
	esac
}

case_run() {
	local name="$1"
	if "$name"; then
		printf 'PASS %s\n' "$name"
		passed=$((passed + 1))
	else
		printf 'FAIL %s\n' "$name"
		failed=$((failed + 1))
	fi
}

layering_rejects_gotd_outside_tgclient() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_gotd_outside_tgclient"
	expect_status 1 && expect_output "L2 violation" && expect_output "internal/core imports github.com/gotd/td/tg"
}

layering_rejects_sqlite_outside_storage() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_sqlite_outside_storage"
	expect_status 1 && expect_output "L3 violation" && expect_output "internal/projection/log imports modernc.org/sqlite"
}

layering_rejects_domain_thirdparty() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_domain_thirdparty"
	expect_status 1 && expect_output "L1 violation" && expect_output "internal/domain/errs imports example.com/thirdparty"
}

layering_rejects_api_importing_internal() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_api_internal"
	expect_status 1 && expect_output "L6 violation" && expect_output "api/adminv1 imports"
}

layering_rejects_root_importing_apps() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_root_imports_apps"
	expect_status 1 && expect_output "L7 violation"
}

layering_rejects_app_importing_internal() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_app_imports_internal"
	expect_status 1 && expect_output "L8 violation" && expect_output "apps/admin imports"
}

layering_accepts_clean_tree() {
	run_script bash "$scripts_dir/layering.sh" "$fixtures/layering_clean"
	expect_status 0 && expect_output "layering: ok" && expect_no_output "violation"
}

nocomments_rejects_two_lines() {
	run_script bash "$scripts_dir/nocomments.sh" "$fixtures/nocomments_two_lines"
	expect_status 1 && expect_output "two.go:4: two consecutive comment lines"
}

nocomments_rejects_block() {
	run_script bash "$scripts_dir/nocomments.sh" "$fixtures/nocomments_block"
	expect_status 1 && expect_output "block.go:3: block comment is not allowed"
}

nocomments_rejects_todo() {
	run_script bash "$scripts_dir/nocomments.sh" "$fixtures/nocomments_todo"
	expect_status 1 && expect_output "todo.go:3: TODO, FIXME and XXX markers are not allowed"
}

nocomments_rejects_commented_out_code() {
	run_script bash "$scripts_dir/nocomments.sh" "$fixtures/nocomments_commented_code"
	expect_status 1 && expect_output "code.go:4: comment looks like commented-out code"
}

nocomments_allows_directives() {
	run_script bash "$scripts_dir/nocomments.sh" "$fixtures/nocomments_directives"
	expect_status 0 && expect_output "nocomments: ok"
}

nocomments_ignores_comment_markers_in_strings() {
	run_script bash "$scripts_dir/nocomments.sh" "$fixtures/nocomments_clean_strings"
	expect_status 0 && expect_output "nocomments: ok"
}

git_quiet() {
	git -c user.name=fixture -c user.email=fixture@example.invalid "$@"
}

worktree_create_remove_roundtrip() {
	local sandbox="$work/roundtrip"
	local repo="$sandbox/repo"
	local wt_root="$sandbox/butterdb-wt"
	local lane_path="$wt_root/01-l4"
	local empty_path="$wt_root/01-l5"
	local picked

	mkdir -p "$repo"
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
	git -C "$repo" init -q -b main
	printf 'seed\n' >"$repo/seed.txt"
	git -C "$repo" add -- seed.txt
	git_quiet -C "$repo" commit -q -m "seed"
	git -C "$repo" branch phase/01-foundation-and-guardrails main

	export BUTTERDB_REPO="$repo" BUTTERDB_WT_ROOT="$wt_root" BUTTERDB_MERGE_TARGET=main

	run_script bash "$scripts_dir/worktree-create.sh" 01 foundation-and-guardrails l4
	expect_status 0 || return 1
	[ -d "$lane_path" ] || { printf '    worktree directory missing\n' >&2; return 1; }
	[ "$(git -C "$lane_path" branch --show-current)" = "phase/01-foundation-and-guardrails-l4" ] || return 1
	[ "$(git -C "$lane_path" rev-parse HEAD)" = "$(git -C "$repo" rev-parse main)" ] || return 1

	run_script bash "$scripts_dir/worktree-create.sh" 01 foundation-and-guardrails l4
	expect_status 1 && expect_output "already exists" || return 1

	run_script bash "$scripts_dir/worktree-create.sh" 01 foundation-and-guardrails l5
	expect_status 0 || return 1

	printf 'lane work\n' >"$lane_path/lane.txt"
	git -C "$lane_path" add -- lane.txt
	git_quiet -C "$lane_path" commit -q -m "add lane file"

	printf 'dirty\n' >"$lane_path/dirty.txt"
	run_script bash "$scripts_dir/worktree-remove.sh" 01 foundation-and-guardrails l4
	expect_status 1 && expect_output "uncommitted" || return 1
	[ -d "$lane_path" ] || return 1
	rm -f "$lane_path/dirty.txt"

	run_script bash "$scripts_dir/worktree-remove.sh" 01 foundation-and-guardrails l4
	expect_status 3 && expect_output "kept branch phase/01-foundation-and-guardrails-l4" || return 1
	[ ! -d "$lane_path" ] || return 1
	git -C "$repo" show-ref --verify --quiet refs/heads/phase/01-foundation-and-guardrails-l4 || return 1

	picked="$(git -C "$repo" rev-parse phase/01-foundation-and-guardrails-l4)"
	git_quiet -C "$repo" cherry-pick "$picked" >/dev/null
	run_script bash "$scripts_dir/worktree-remove.sh" 01 foundation-and-guardrails l4
	expect_status 0 && expect_output "branch deleted" || return 1
	if git -C "$repo" show-ref --verify --quiet refs/heads/phase/01-foundation-and-guardrails-l4; then
		printf '    picked branch was not deleted\n' >&2
		return 1
	fi

	run_script bash "$scripts_dir/worktree-remove.sh" 01 foundation-and-guardrails l5
	expect_status 0 && expect_output "branch deleted" || return 1
	[ ! -d "$empty_path" ] || return 1

	[ -z "$(git -C "$repo" status --porcelain --untracked-files=all)" ] || return 1
	[ "$(git -C "$repo" worktree list --porcelain | grep -c '^worktree ')" -eq 1 ] || return 1
}

case_run layering_rejects_gotd_outside_tgclient
case_run layering_rejects_sqlite_outside_storage
case_run layering_rejects_domain_thirdparty
case_run layering_rejects_api_importing_internal
case_run layering_rejects_root_importing_apps
case_run layering_rejects_app_importing_internal
case_run layering_accepts_clean_tree
case_run nocomments_rejects_two_lines
case_run nocomments_rejects_block
case_run nocomments_rejects_todo
case_run nocomments_rejects_commented_out_code
case_run nocomments_allows_directives
case_run nocomments_ignores_comment_markers_in_strings
case_run worktree_create_remove_roundtrip

printf '%s passed, %s failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
