#!/bin/sh
# Builds the universal (Apple Silicon + Intel) macOS client: bin/Sonoryx.app
# and, with --release, the release assets: bin/Sonoryx-macos.zip (update and
# download archive) and bin/Sonoryx-macos.dmg (drag-to-Applications image for
# manual installs; the updater cannot mount it).
# The frontend (cmd/desktop/frontend/dist) and Wails bindings must be built
# first; Taskfile.yml and release.yml do that.
#
# --dev builds only the host architecture without the production tag and
# without stripping, for `task build DEV=true`.
#
# Usage: scripts/build-macos-app.sh [--release | --dev]
set -eu
mode=${1:-}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
min_macos=12.0
rnnoise="$root/.cache/rnnoise-darwin"
sh scripts/build-rnnoise-macos.sh "$rnnoise"

mkdir -p bin
build() { # build <goarch> <output> <go build flags...>
    goarch=$1
    output=$2
    shift 2
    clang_arch=$goarch
    [ "$goarch" = amd64 ] && clang_arch=x86_64
    echo "building desktop client for darwin/$goarch"
    CGO_ENABLED=1 GOOS=darwin GOARCH=$goarch \
    MACOSX_DEPLOYMENT_TARGET=$min_macos \
    CC="clang -arch $clang_arch" \
    CGO_CFLAGS="-O2 -mmacosx-version-min=$min_macos -I$rnnoise/include" \
    CGO_LDFLAGS="-mmacosx-version-min=$min_macos -L$rnnoise/lib -lrnnoise" \
        go build "$@" -o "$output" ./cmd/desktop
}
if [ "$mode" = "--dev" ]; then
    # go build refuses to overwrite a universal (lipo) binary in place.
    build "$(go env GOARCH)" bin/.Sonoryx-dev -buildvcs=false -gcflags=all=-l
    mv -f bin/.Sonoryx-dev bin/Sonoryx
else
    for goarch in arm64 amd64; do
        build "$goarch" "bin/.Sonoryx-$goarch" -tags production -trimpath -ldflags="-w -s"
    done
    lipo -create bin/.Sonoryx-arm64 bin/.Sonoryx-amd64 -output bin/Sonoryx
    rm -f bin/.Sonoryx-arm64 bin/.Sonoryx-amd64
fi
lipo -info bin/Sonoryx

sh scripts/package-macos.sh bin/Sonoryx bin/Sonoryx.app
if [ "$mode" = "--release" ]; then
    # ditto keeps the bundle layout, permissions and signature intact. No
    # resource forks or xattrs: Go's zip reader (Wails) would write their
    # "._*" entries into the bundle and break its code signature. The
    # updater unpacks this archive and swaps Sonoryx.app in place.
    rm -f bin/Sonoryx-macos.zip
    ditto -c -k --norsrc --noextattr --noacl --keepParent bin/Sonoryx.app bin/Sonoryx-macos.zip
    echo "archived bin/Sonoryx-macos.zip"

    # The image holds the app and an /Applications link, so a manual install
    # lands where the updater can replace the bundle (an app started from
    # Downloads runs translocated, read-only).
    stage=$(mktemp -d)
    trap 'rm -rf "$stage"' EXIT
    ditto --norsrc --noextattr --noacl bin/Sonoryx.app "$stage/Sonoryx.app"
    ln -s /Applications "$stage/Applications"
    rm -f bin/Sonoryx-macos.dmg
    hdiutil create -volname Sonoryx -srcfolder "$stage" -fs HFS+ -format UDZO -imagekey zlib-level=9 -ov bin/Sonoryx-macos.dmg >/dev/null
    hdiutil verify bin/Sonoryx-macos.dmg >/dev/null
    echo "imaged bin/Sonoryx-macos.dmg"
fi
