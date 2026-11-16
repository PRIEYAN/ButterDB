#!/usr/bin/env bash
set -euo pipefail

if [ $# -gt 1 ]; then
	printf 'usage: layering.sh [repository-root]\n' >&2
	exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if [ $# -eq 1 ]; then
	root="$(cd "$1" && pwd -P)"
else
	root="$(cd "$script_dir/.." && pwd -P)"
fi

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

checker='
function isstd(p,   first) {
	first = p
	sub(/\/.*/, "", first)
	return index(first, ".") == 0
}
function inmod(p) { return p == mod || index(p, mod "/") == 1 }
function rel(p) { if (p == mod) return "."; return substr(p, length(mod) + 2) }
function under(r, base) { return r == base || index(r, base "/") == 1 }
function isdomain(r) {
	return under(r, "internal/domain") || under(r, "internal/wire") || under(r, "internal/crypto/format")
}
function violate(rule, pkg, imp, msg) {
	printf("layering: %s violation: %s imports %s: %s\n", rule, pkg, imp, msg)
	bad++
}
function check(pkg, imp,   r, ir) {
	if (imp == "C" || (isstd(imp) && !inmod(imp))) return
	r = rel(pkg)
	ir = rel(imp)
	if ((imp == "github.com/gotd/td" || index(imp, "github.com/gotd/td/") == 1) && !under(r, "internal/tgclient"))
		violate("L2", pkg, imp, "only internal/tgclient/... may import github.com/gotd/td")
	if ((imp == "modernc.org/sqlite" || index(imp, "modernc.org/sqlite/") == 1) && !under(r, "internal/storage"))
		violate("L3", pkg, imp, "only internal/storage/... may import modernc.org/sqlite")
	if (kind == "root") {
		if (isdomain(r))
			violate("L1", pkg, imp, "internal/domain, internal/wire and internal/crypto/format may import the standard library only")
		if (under(r, "api") && !(inmod(imp) && under(ir, "api")))
			violate("L6", pkg, imp, "api/... may import the standard library and api/... only")
		if (inmod(imp) && under(ir, "apps"))
			violate("L7", pkg, imp, "the root module must not import apps/...")
	}
	if (kind == "app" && !inmod(imp) && (imp == rootmod || index(imp, rootmod "/") == 1) && !under(substr(imp, length(rootmod) + 2), "api"))
		violate("L8", pkg, imp, "apps modules may import only api/... from the root module")
}
function flush(   i, key) {
	if (path != "" && !deponly) {
		for (i = 1; i <= n; i++) {
			key = path SUBSEP imports[i]
			if (key in seen) continue
			seen[key] = 1
			check(path, imports[i])
		}
	}
	path = ""
	deponly = 0
	n = 0
	section = 0
}
BEGIN { bad = 0 }
/^\{$/ { flush(); next }
/^\t"ImportPath": / { s = $0; sub(/^\t"ImportPath": "/, "", s); sub(/",?$/, "", s); path = s; next }
/^\t"DepOnly": true/ { deponly = 1; next }
/^\t"(Imports|TestImports|XTestImports)": \[$/ { section = 1; next }
/^\t\]/ { section = 0; next }
/^\t\t"/ {
	if (section) {
		s = $0
		sub(/^\t\t"/, "", s)
		sub(/",?$/, "", s)
		imports[++n] = s
	}
	next
}
END { flush(); exit bad > 0 ? 1 : 0 }
'

list_packages() {
	local dir="$1"
	if ! (cd "$dir" && go list -e -deps -json=ImportPath,Imports,TestImports,XTestImports,DepOnly ./... >"$work/list.json" 2>"$work/list.err"); then
		printf 'layering: go list failed in %s\n' "$dir" >&2
		cat "$work/list.err" >&2
		exit 2
	fi
}

check_module() {
	local dir="$1" kind="$2" rootmod="$3" mod
	if [ "$kind" = root ]; then
		export GOWORK=off
	fi
	if ! mod="$(cd "$dir" && go list -m -f '{{.Dir}}|{{.Path}}' 2>"$work/mod.err" | awk -F'|' -v d="$dir" '$1 == d { print $2; found = 1 } END { exit found ? 0 : 1 }')"; then
		printf 'layering: cannot read module path in %s\n' "$dir" >&2
		cat "$work/mod.err" >&2
		exit 2
	fi
	list_packages "$dir"
	awk -v mod="$mod" -v kind="$kind" -v rootmod="$rootmod" "$checker" "$work/list.json" >>"$work/violations.txt" || true
	printf '%s\n' "$mod"
}

: >"$work/violations.txt"

if [ ! -f "$root/go.mod" ]; then
	printf 'layering: no go.mod in %s\n' "$root" >&2
	exit 2
fi

root_module="$(check_module "$root" root "")"
module_count=1

for app in "$root"/apps/*/; do
	[ -f "${app}go.mod" ] || continue
	check_module "${app%/}" app "$root_module" >/dev/null
	module_count=$((module_count + 1))
done

if [ -s "$work/violations.txt" ]; then
	sort -u "$work/violations.txt"
	printf 'layering: FAILED, %s violation(s)\n' "$(sort -u "$work/violations.txt" | wc -l | tr -d ' ')"
	exit 1
fi

printf 'layering: ok (%s module(s) checked)\n' "$module_count"
