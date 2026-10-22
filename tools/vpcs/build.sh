#!/bin/sh
# Builds the VPCS that Gantry carries (internal/vpcsbin/bin): VPCS 0.8.5 of
# GNS3 (BSD license, internal/vpcsbin/bin/COPYING), static, for Linux amd64.
# Run on a Linux amd64 machine with gcc and git.
set -eu
out=$(cd "$(dirname "$0")/../../internal/vpcsbin/bin" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git clone --depth 1 --branch v0.8.5 https://github.com/GNS3/vpcs.git "$tmp/vpcs"
cd "$tmp/vpcs/src"
make -f Makefile.linux CCOPT=-O2 LDFLAGS=-static
strip vpcs
gzip -9c vpcs >"$out/vpcs-linux-amd64.gz"
cp ../COPYING "$out/COPYING"
echo "ok: $out/vpcs-linux-amd64.gz"
