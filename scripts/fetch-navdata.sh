#!/bin/sh
# Downloads FlightGear's X-Plane-format waypoint and navaid files into data/navdata/ so that
# waypoint names in CPDLC position reports can be placed on the map.
#
# The data is GPL-licensed (Robin A. Peel's X-Plane navigation data, via FlightGear's fgdata) and
# is from 2013: it lacks newer waypoints and some names have been reused. It is kept out of git
# (data/ is ignored) and the app works without it, falling back to positions learned from the
# aircraft's own ADS-C reports and to estimates.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
dir=$root/data/navdata
base=https://gitlab.com/flightgear/fgdata/-/raw/next/Navaids
mkdir -p "$dir"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for f in fix.dat.gz nav.dat.gz; do
	echo "fetching $f"
	curl -fsSL --max-time 180 -o "$tmp/$f" "$base/$f"
	gzip -t "$tmp/$f"                 # a truncated or non-gzip download must not replace a good file
	mv "$tmp/$f" "$dir/$f"
done
echo "done: $dir"
gzip -dc "$dir/fix.dat.gz" | sed -n 2p
