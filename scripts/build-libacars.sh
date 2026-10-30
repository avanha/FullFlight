#!/bin/sh
# Builds libacars (needs cmake, pkgconf, jansson, zlib, libxml2) into third_party/install.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
src=$root/third_party/libacars
if [ ! -d "$src" ]; then
	mkdir -p "$root/third_party"
	git clone --depth 1 https://github.com/szpajder/libacars.git "$src"
fi
rm -rf "$src/build"
mkdir "$src/build"
cd "$src/build"
cmake .. \
	-DCMAKE_INSTALL_PREFIX="$root/third_party/install" \
	-DCMAKE_INSTALL_RPATH="$root/third_party/install/lib" \
	-DCMAKE_POLICY_VERSION_MINIMUM=3.5
make -j4
make install
