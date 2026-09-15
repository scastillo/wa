#!/bin/sh
# Build the wa release binaries for the version in .claude-plugin/plugin.json.
# With --publish, also create the GitHub release v<version> with the binaries
# and SHA256SUMS. The bin/wa launcher downloads exactly these assets.
set -eu

repo=scastillo/wa

die() {
	printf 'release: %s\n' "$*" >&2
	exit 1
}

cd "$(dirname -- "$0")/.."

publish=false
case ${1:-} in
'') ;;
--publish) publish=true ;;
*) die "usage: scripts/release.sh [--publish]" ;;
esac

version=$(sed -n 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' .claude-plugin/plugin.json | head -n 1)
[ -n "$version" ] || die "cannot read the version from .claude-plugin/plugin.json"
[ -z "$(git status --porcelain)" ] || die "the working tree has changes. Commit them first"
grep -q "^## \[$version\]" CHANGELOG.md || die "CHANGELOG.md has no entry for $version"

GOTOOLCHAIN=auto go test ./...

mkdir -p dist
for arch in arm64 amd64; do
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch GOTOOLCHAIN=auto \
		go build -trimpath -ldflags '-s -w' -o "dist/wa-darwin-$arch" ./cmd/wa
done
(cd dist && shasum -a 256 wa-darwin-arm64 wa-darwin-amd64 >SHA256SUMS)
cat dist/SHA256SUMS
awk -v h="## [$version]" 'index($0, h) == 1 { on = 1; next } on && /^## \[/ { exit } on' CHANGELOG.md >dist/NOTES.md

if [ "$publish" = false ]; then
	echo "Built wa $version in dist/. Run with --publish to create release v$version."
	exit 0
fi

head=$(git rev-parse HEAD)
git fetch --quiet origin main
[ "$head" = "$(git rev-parse origin/main)" ] || die "HEAD is not origin/main. Push main first"
gh release create "v$version" -R "$repo" --target "$head" --title "wa $version" \
	--notes-file dist/NOTES.md dist/wa-darwin-arm64 dist/wa-darwin-amd64 dist/SHA256SUMS
