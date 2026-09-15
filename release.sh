#!/usr/bin/env bash

set -euo pipefail

readonly repo="canonical/spread-plus"
readonly remote="origin"
readonly default_version="$(date -u +%Y.%m.%d)"

usage() {
	cat <<EOF
Usage: ./release.sh <prepare|publish|verify> [VERSION]

Commands:
  prepare  Update snapcraft.yaml, run checks, push a release branch, and open a PR
  publish  Publish the GitHub Release after the release PR has been merged
  verify   Download and verify the release assets and local binary

VERSION defaults to the current UTC date: ${default_version}
Use YYYY.MM.DD.N for additional releases on the same day, starting with .1.
EOF
}

die() {
	echo "error: $*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null || die "required command not found: $1"
}

require_clean_tree() {
	[[ -z "$(git status --short)" ]] || die "working tree must be clean"
}

validate_version() {
	local version="$1"
	local date_version
	local normalized

	[[ "${version}" =~ ^([0-9]{4}\.[0-9]{2}\.[0-9]{2})(\.[1-9][0-9]*)?$ ]] ||
		die "version must use YYYY.MM.DD or YYYY.MM.DD.N format"
	date_version="${BASH_REMATCH[1]}"
	normalized=$(date -u -d "${date_version//./-}" +%Y.%m.%d 2>/dev/null) ||
		die "version is not a valid date: ${version}"
	[[ "${normalized}" == "${date_version}" ]] || die "version is not a valid date: ${version}"
}

require_unreleased() {
	local version="$1"
	local status

	if git ls-remote --exit-code --tags "${remote}" "refs/tags/${version}" >/dev/null 2>&1; then
		die "version ${version} is already tagged on ${remote}"
	else
		status=$?
	fi
	[[ ${status} -eq 2 ]] || die "could not check tags on ${remote}"
}

snapcraft_version() {
	awk '$1 == "version:" { print $2; exit }' snapcraft.yaml
}

prepare() {
	local version="$1"
	local branch="release-${version}"
	local binary

	require_command gh
	require_command go
	require_clean_tree
	gh auth status >/dev/null
	require_unreleased "${version}"

	git switch main
	git pull --ff-only "${remote}" main
	git show-ref --verify --quiet "refs/heads/${branch}" &&
		die "local branch already exists: ${branch}"
	git switch -c "${branch}"

	sed -i -E "0,/^version: .*/s//version: ${version}/" snapcraft.yaml
	[[ "$(snapcraft_version)" == "${version}" ]] || die "failed to update snapcraft.yaml"

	go test ./...
	binary=$(mktemp "${TMPDIR:-/tmp}/spread-plus.XXXXXX")
	trap 'rm -f "${binary}"' RETURN
	go build -ldflags "-X main.version=${version}" -o "${binary}" ./cmd/spread
	[[ "$("${binary}" --version)" == "${version}" ]] || die "built binary reported the wrong version"
	rm -f "${binary}"
	trap - RETURN

	git add snapcraft.yaml
	git commit -m "Prepare ${version} release"
	git push -u "${remote}" "${branch}"
	gh pr create --repo "${repo}" --fill
}

publish() {
	local version="$1"

	require_command gh
	require_clean_tree
	gh auth status >/dev/null
	git switch main
	git pull --ff-only "${remote}" main
	require_clean_tree
	[[ "$(snapcraft_version)" == "${version}" ]] ||
		die "snapcraft.yaml version is $(snapcraft_version), expected ${version}"
	require_unreleased "${version}"

	gh release create "${version}" \
		--repo "${repo}" \
		--target main \
		--title "Spread Plus ${version}" \
		--generate-notes
}

verify() {
	local version="$1"
	local architecture
	local output_dir="spread-plus-${version}"
	local assets

	require_command gh
	require_command sha256sum
	require_command tar
	gh auth status >/dev/null
	[[ ! -e "${output_dir}" ]] || die "verification directory already exists: ${output_dir}"

	assets=$(gh release view "${version}" --repo "${repo}" --json assets --jq '.assets[].name')
	for asset in \
		spread-plus-amd64.tar.gz \
		spread-plus-arm64.tar.gz \
		spread-plus-amd64.snap \
		spread-plus-arm64.snap \
		SHA256SUMS; do
		grep -Fxq "${asset}" <<<"${assets}" || die "release asset is missing: ${asset}"
	done

	mkdir "${output_dir}"
	gh release download "${version}" --repo "${repo}" --dir "${output_dir}"
	(
		cd "${output_dir}"
		sha256sum --check SHA256SUMS

		case "$(uname -m)" in
			x86_64) architecture=amd64 ;;
			aarch64 | arm64) architecture=arm64 ;;
			*) die "unsupported local architecture: $(uname -m)" ;;
		esac

		tar -xzf "spread-plus-${architecture}.tar.gz"
		[[ "$(./spread-plus --version)" == "${version}" ]] ||
			die "released binary reported the wrong version"
	)

	echo "Release ${version} verified in ${output_dir}"
}

[[ $# -ge 1 && $# -le 2 ]] || {
	usage >&2
	exit 2
}

command="$1"
version="${2:-${default_version}}"
validate_version "${version}"

case "${command}" in
	prepare) prepare "${version}" ;;
	publish) publish "${version}" ;;
	verify) verify "${version}" ;;
	-h | --help | help) usage ;;
	*)
		usage >&2
		die "unknown command: ${command}"
		;;
esac