#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
root="$(cd "$script_dir/.." && pwd -P)"
cd "$root"

export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

skipped=()

fail() {
	printf 'verify: FAIL at step "%s"\n' "$1" >&2
	exit 1
}

step() {
	local name="$1"
	shift
	printf 'verify: %s\n' "$name"
	"$@" || fail "$name"
}

check_gofmt() {
	local listed
	listed="$(gofmt -l .)"
	if [ -n "$listed" ]; then
		printf 'verify: gofmt reports unformatted files:\n%s\n' "$listed" >&2
		return 1
	fi
}

root_go() {
	env GOWORK=off CGO_ENABLED="$1" go "${@:2}"
}

have_cc() {
	command -v "${CC:-cc}" >/dev/null 2>&1
}

has_packages() {
	[ -n "$(env GOWORK=off CGO_ENABLED=0 go list ./... 2>/dev/null)" ]
}

step "gofmt -l ." check_gofmt

if has_packages; then
	step "go build" root_go 0 build -mod=readonly ./...
	step "go vet" root_go 0 vet -mod=readonly ./...
	step "go test" root_go 0 test -mod=readonly -count=1 ./...
	if have_cc; then
		step "go test -race" root_go 1 test -mod=readonly -race -count=1 ./...
	else
		skipped+=("go test -race (no C compiler)")
	fi
else
	skipped+=("go build, vet, test and race (root module has no Go packages)")
fi

step "go mod verify" root_go 0 mod verify
step "layering.sh" bash "$script_dir/layering.sh"
step "nocomments.sh" bash "$script_dir/nocomments.sh"

for app in apps/*/; do
	[ -f "${app}go.mod" ] || continue
	if ! have_cc; then
		skipped+=("${app%/} vet and test (no C compiler)")
		continue
	fi
	step "${app%/} go vet" env CGO_ENABLED=1 go -C "${app%/}" vet -mod=readonly ./...
	step "${app%/} go test" env CGO_ENABLED=1 go -C "${app%/}" test -mod=readonly -count=1 ./...
done

if [ "${#skipped[@]}" -gt 0 ]; then
	for item in "${skipped[@]}"; do
		printf 'verify: skipped %s\n' "$item"
	done
fi

printf 'verify: ok\n'
