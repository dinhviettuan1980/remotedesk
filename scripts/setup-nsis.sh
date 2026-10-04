#!/usr/bin/env bash
# One-time: build NSIS's makensis for macOS into build/nsis (≈1 min), for the
# Windows installer. Homebrew's makensis builds LLVM/Rust/mingw from source on
# some macOS versions; this only compiles makensis itself and takes the
# Windows stubs/plugins from the official NSIS zip.
set -euo pipefail
cd "$(dirname "$0")/.."
V=3.13
D=build/nsis
mkdir -p "$D" && cd "$D"
[ -f src.tar.bz2 ] || curl -fsSL -o src.tar.bz2 "https://downloads.sourceforge.net/project/nsis/NSIS%203/$V/nsis-$V-src.tar.bz2"
[ -f win.zip ] || curl -fsSL -o win.zip "https://downloads.sourceforge.net/project/nsis/NSIS%203/$V/nsis-$V.zip"
tar xjf src.tar.bz2 && unzip -q -o win.zip
[ -d venv ] || { python3 -m venv venv && venv/bin/pip install -q scons; }
(cd "nsis-$V-src" && ../venv/bin/scons -j4 SKIPSTUBS=all SKIPPLUGINS=all SKIPUTILS=all SKIPMISC=all \
  NSIS_CONFIG_CONST_DATA_PATH=no NSIS_MAX_STRLEN=1024 makensis >/dev/null)
cp "nsis-$V-src/build/urelease/makensis/makensis" "nsis-$V/"
LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 NSISDIR="$PWD/nsis-$V" "nsis-$V/makensis" -VERSION >/dev/null && echo "makensis ready: $D/nsis-$V/makensis"
