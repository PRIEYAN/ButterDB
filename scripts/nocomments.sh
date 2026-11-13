#!/usr/bin/env bash
set -euo pipefail

if [ $# -gt 1 ]; then
	printf 'usage: nocomments.sh [repository-root]\n' >&2
	exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [ $# -eq 1 ]; then
	root="$(cd "$1" && pwd -P)"
else
	root="$(cd "$script_dir/.." && pwd -P)"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

scanner='
function trim(s) {
	sub(/^[ \t]+/, "", s)
	sub(/[ \t]+$/, "", s)
	return s
}
function report(msg) {
	printf("nocomments: %s:%d: %s\n", FILENAME, FNR, msg)
	bad++
}
function looks_like_code(t) {
	if (t ~ /^[A-Za-z_][A-Za-z0-9_.]*(, [A-Za-z_][A-Za-z0-9_.]*)* (:=|=|\+=|-=) /) return 1
	if (t ~ /^[A-Za-z_][A-Za-z0-9_.]*(\+\+|--)$/) return 1
	if (t ~ /^(if|for|switch|select|func)( |\().*\{$/) return 1
	if (t ~ /^\}( else.*)?$/ || t ~ /^\}\)$/) return 1
	if (t ~ /^(return|defer|go) [A-Za-z_][A-Za-z0-9_.]*\(([A-Za-z0-9_.&*"]+(, )?)*\)$/) return 1
	if (t ~ /^return$/ || t ~ /^return (nil|err|true|false)(, ?[A-Za-z_.]+)*$/) return 1
	if (t ~ /^[A-Za-z_][A-Za-z0-9_.]*\(([A-Za-z0-9_.&*"]+(, )?)*\)$/) return 1
	if (t ~ /^(var|const) [A-Za-z_][A-Za-z0-9_]*( [A-Za-z0-9_.*\[\]]+)? = /) return 1
	if (t ~ /^type [A-Za-z_][A-Za-z0-9_]* (struct|interface)( \{)?$/) return 1
	if (t ~ /^(case .*|default):$/) return 1
	if (t ~ /^if .* (==|!=) nil/) return 1
	return 0
}
function check_text(t) {
	if (t ~ /(^|[^A-Za-z0-9_])(TODO|FIXME|XXX)([^A-Za-z0-9_]|$)/) report("TODO, FIXME and XXX markers are not allowed")
}
FNR == 1 { state = 0; run = 0 }
{
	line = $0
	n = length(line)
	i = 1
	codeseen = 0
	incomment = 0
	text = ""
	while (i <= n) {
		c = substr(line, i, 1)
		if (state == 1) {
			if (c == "`") state = 0
			i++
			continue
		}
		if (state == 2) {
			idx = index(substr(line, i), "*/")
			if (idx == 0) {
				i = n + 1
			} else {
				i += idx + 1
				state = 0
			}
			continue
		}
		if (c == " " || c == "\t") { i++; continue }
		if (c == "\"" || c == sq) {
			codeseen = 1
			q = c
			i++
			while (i <= n) {
				ch = substr(line, i, 1)
				if (ch == "\\") { i += 2; continue }
				i++
				if (ch == q) break
			}
			continue
		}
		if (c == "`") { codeseen = 1; state = 1; i++; continue }
		if (c == "/" && substr(line, i + 1, 1) == "/") {
			incomment = 1
			text = substr(line, i + 2)
			break
		}
		if (c == "/" && substr(line, i + 1, 1) == "*") {
			report("block comment is not allowed")
			blockline = 1
			state = 2
			i += 2
			continue
		}
		codeseen = 1
		i++
	}
	if (blockline) { blockline = 0; run = 0; next }
	if (!incomment) { run = 0; next }
	check_text(text)
	if (codeseen) { run = 0; next }
	if (substr(text, 1, 3) == "go:" || text ~ /^ ?\+build( |$)/) { run = 0; next }
	run++
	if (run == 2) report("two consecutive comment lines")
	if (looks_like_code(trim(text))) report("comment looks like commented-out code")
}
END { exit bad > 0 ? 1 : 0 }
'

in_git() {
	git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1
}

list_tracked() {
	local pattern="$1"
	if in_git; then
		git -C "$root" ls-files -z --cached --others --exclude-standard -- "$pattern"
	else
		(cd "$root" && find . -name "$pattern" -not -path '*/.git/*' -print0)
	fi
}

: >"$work/modules"
printf '.\n' >>"$work/modules"
while IFS= read -r -d '' gomod; do
	gomod="${gomod#./}"
	case "$gomod" in
	scripts/test/fixtures/*) continue ;;
	esac
	[ -f "$root/$gomod" ] || continue
	dir="$(dirname "$gomod")"
	[ "$dir" = "." ] && continue
	printf '%s\n' "$dir" >>"$work/modules"
done < <(list_tracked 'go.mod')

module_dirs=()
while IFS= read -r dir; do
	module_dirs+=("$dir")
done <"$work/modules"

owner_index() {
	local file="$1" best=0 best_len=0 i dir
	for i in "${!module_dirs[@]}"; do
		dir="${module_dirs[$i]}"
		[ "$dir" = "." ] && continue
		case "$file" in
		"$dir"/*)
			if [ ${#dir} -gt "$best_len" ]; then
				best=$i
				best_len=${#dir}
			fi
			;;
		esac
	done
	printf '%s' "$best"
}

for i in "${!module_dirs[@]}"; do
	: >"$work/files.$i"
done

file_count=0
while IFS= read -r -d '' file; do
	file="${file#./}"
	case "$file" in
	scripts/test/fixtures/*) continue ;;
	esac
	[ -f "$root/$file" ] || continue
	idx="$(owner_index "$file")"
	printf '%s\0' "$file" >>"$work/files.$idx"
	file_count=$((file_count + 1))
done < <(list_tracked '*.go')

: >"$work/violations"
: >"$work/awk.err"
for i in "${!module_dirs[@]}"; do
	[ -s "$work/files.$i" ] || continue
	(cd "$root" && xargs -0 awk -v sq="'" "$scanner" <"$work/files.$i") >>"$work/violations" 2>>"$work/awk.err" || true
done

if [ -s "$work/awk.err" ]; then
	printf 'nocomments: scanner error\n' >&2
	cat "$work/awk.err" >&2
	exit 2
fi

if [ -s "$work/violations" ]; then
	cat "$work/violations"
	printf 'nocomments: FAILED, %s violation(s)\n' "$(wc -l <"$work/violations" | tr -d ' ')"
	exit 1
fi

printf 'nocomments: ok (%s file(s), %s module(s))\n' "$file_count" "${#module_dirs[@]}"
