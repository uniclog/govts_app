#!/bin/sh
# Builds a universal (arm64 + x86_64) static librnnoise for the macOS client.
# go-rnnoise ships prebuilt libraries only for Linux and Windows, so cgo needs
# this one through CGO_CFLAGS/CGO_LDFLAGS (see Taskfile.yml and release.yml).
#
# Usage: scripts/build-rnnoise-macos.sh [prefix]   (default: .cache/rnnoise-darwin)
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
prefix=${1:-"$root/.cache/rnnoise-darwin"}
version=0.2
url="https://github.com/xiph/rnnoise/releases/download/v$version/rnnoise-$version.tar.gz"
sha256=90fce4b00b9ff24c08dbfe31b82ffd43bae383d85c5535676d28b0a2b11c0d37
min_macos=12.0

if [ -f "$prefix/lib/librnnoise.a" ] && [ -f "$prefix/include/rnnoise.h" ]; then
    echo "rnnoise already built: $prefix"
    exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL "$url" -o "$work/rnnoise.tar.gz"
echo "$sha256  $work/rnnoise.tar.gz" | shasum -a 256 -c -

jobs=$(sysctl -n hw.ncpu 2>/dev/null || echo 4)
for arch in arm64 x86_64; do
    mkdir -p "$work/$arch"
    tar -xzf "$work/rnnoise.tar.gz" -C "$work/$arch" --strip-components=1
    # The 0.2 tarball omits os_support.h, which the ARM NEON path includes.
    if [ ! -f "$work/$arch/src/os_support.h" ]; then
        cat > "$work/$arch/src/os_support.h" <<'SHIM'
#ifndef OS_SUPPORT_H
#define OS_SUPPORT_H
#include "common.h"
#include "opus_types.h"
#ifndef OPUS_CLEAR
#define OPUS_CLEAR(dst, n) RNN_CLEAR(dst, n)
#endif
#ifndef OPUS_COPY
#define OPUS_COPY(dst, src, n) RNN_COPY(dst, src, n)
#endif
#ifndef OPUS_MOVE
#define OPUS_MOVE(dst, src, n) RNN_MOVE(dst, src, n)
#endif
#endif
SHIM
    fi
    host=aarch64-apple-darwin
    [ "$arch" = x86_64 ] && host=x86_64-apple-darwin
    (
        cd "$work/$arch"
        ./configure --host="$host" --prefix="$work/$arch-out" \
            --disable-shared --enable-static --disable-examples --disable-doc \
            CC="clang -arch $arch" CFLAGS="-O2 -mmacosx-version-min=$min_macos" >/dev/null
        make -j"$jobs" >/dev/null
        make install >/dev/null
    )
done

mkdir -p "$prefix/lib" "$prefix/include"
lipo -create "$work/arm64-out/lib/librnnoise.a" "$work/x86_64-out/lib/librnnoise.a" -output "$prefix/lib/librnnoise.a"
cp "$work/arm64-out/include/rnnoise.h" "$prefix/include/rnnoise.h"
lipo -info "$prefix/lib/librnnoise.a"
