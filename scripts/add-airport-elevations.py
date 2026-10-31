#!/usr/bin/env python3
"""Add (or refresh) the elevation_ft column of internal/airports/airports.csv.

Usage: scripts/add-airport-elevations.py path/to/ourairports/airports.csv

The source is OurAirports (public domain): https://davidmegginson.github.io/ourairports-data/airports.csv
Only the airports already in our file are kept, in the same order; elevations come from the source,
matched on ICAO code (its icao_code, else its ident). An airport the source has no elevation for gets an
empty one. Our columns: icao,iata,name,city,lat,lon,elevation_ft
"""
import csv
import os
import sys

OURS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "internal", "airports", "airports.csv")


def main(src):
    elev = {}
    with open(src, encoding="utf-8", newline="") as f:
        for r in csv.DictReader(f):
            code = r["icao_code"] or r["ident"]
            if r["elevation_ft"].strip():
                try:
                    elev[code] = str(round(float(r["elevation_ft"])))
                except ValueError:
                    pass
    with open(OURS, encoding="utf-8", newline="") as f:
        rows = list(csv.reader(f))
    head, body = rows[0][:6], rows[1:]
    out = [head + ["elevation_ft"]] + [r[:6] + [elev.get(r[0], "")] for r in body]
    with open(OURS, "w", encoding="utf-8", newline="") as f:
        csv.writer(f, lineterminator="\r\n").writerows(out)
    have = sum(1 for r in out[1:] if r[6])
    print(f"{len(body)} airports, {have} with an elevation, {len(body) - have} without")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(sys.argv[1])
