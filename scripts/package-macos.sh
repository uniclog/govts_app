#!/bin/sh
# Wraps the desktop binary into bin/Sonoryx.app. macOS grants the microphone,
# screen recording and system audio recording to an app bundle by its
# Info.plist and signature; a bare binary inherits the terminal's access and
# a process tap then records silence.
#
# Usage: scripts/package-macos.sh <binary> <output.app>
set -eu
binary=$1
app=$2
root=$(cd "$(dirname "$0")/.." && pwd)
version=$(sed -n 's/.*"clientVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$root/version/release.json")

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$binary" "$app/Contents/MacOS/Sonoryx"
sed "s/@VERSION@/$version/g" "$root/build/darwin/Info.plist" > "$app/Contents/Info.plist"
if [ -f "$root/build/darwin/icon.icns" ]; then
    cp "$root/build/darwin/icon.icns" "$app/Contents/Resources/icon.icns"
fi
# Ad-hoc signature: enough for local TCC prompts. macOS asks again after a
# rebuild because the signature changes.
codesign --force --sign - "$app"
echo "packaged $app"
