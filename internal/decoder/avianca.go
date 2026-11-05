package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Avianca's 787s send position and takeoff reports in four formats of their own, none of them documented. All are read
// from samples (N781AV, October and November 2026) and checked against the aircraft's own track, so the fields are named
// for what they matched, and the ones that were not understood are left out.

// A position as these reports write it: degrees, then minutes in tenths (N40382 is 40 38.2 N).
func tenthMinutes(ns, latDeg, latMin, ew, lonDeg, lonMin string) (lat, lon float64) {
	lat = float64(atoi(latDeg)) + float64(atoi(latMin))/10/60
	lon = float64(atoi(lonDeg)) + float64(atoi(lonMin))/10/60
	if ns == "S" {
		lat = -lat
	}
	if ew == "W" {
		lon = -lon
	}
	return lat, lon
}

// A time of day, for a position placed on the day it was received (see BasisDay).
func daySecs(hh, mm, ss int) (float64, bool) {
	if hh > 23 || mm > 59 || ss > 59 {
		return 0, false
	}
	return float64(hh*3600 + mm*60 + ss), true
}

func flightLevel(ft float64) string { return fmt.Sprintf("FL%03.0f", ft/100) }

func atof(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

func groupThousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// The position report on label 83, a cousin of the Delta one (see posrpt.go) with the time as hh:mm and the day left out:
//
//	3N01 POSRPT 4907/LEMD/SKBO .N781AV/03A17:11
//	/WPY AVILA/NWYP LONGA /HDG -88.79/MCH    .47
//	/POS N40382 W003548/FL 12205/TAS 303/SAT +  1
//	/SWND     7/DWND -  14/FOB 53800/ETA 0226
//
// The flight, route and tail; the waypoint passed and the next; the position and the time; the altitude in feet (12205 is
// 12,205 ft just after takeoff, 38328 is cruise), true airspeed, the temperature, the wind speed and the direction it blows
// from (negative for 180 to 360, as in the Delta report: the ground speeds the track gave agree with it), fuel on board in kg,
// and the ETA at the destination (hhmm UTC). The heading is a number that makes no sense and is not read. "03A" is not a date.
var aviancaPosRe = regexp.MustCompile(`^3N01 POSRPT (\d{3,4})/([A-Z]{4})/([A-Z]{4}) \.([A-Z0-9-]+)/\d{2}[A-Z](\d{2}):(\d{2}) ?` +
	`/WPY ?([^/]*?) ?/NWYP ?([^/]*?) ?/HDG ?[^/]*/MCH ?(\d*\.\d+) ?` +
	`/POS ([NS])(\d{2})(\d{3}) ([EW])(\d{3})(\d{3})/FL ?(\d+)/TAS ?(\d+)/SAT ?([+-]?) ?(\d+) ?` +
	`/SWND ?(\d+)/DWND ?(-?) ?(\d+)/FOB ?(\d+)/ETA ?(\d{4}|-*)`)

func parseAviancaPOSRPT(text string) (opsParsed, bool) {
	m := aviancaPosRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	secs, ok := daySecs(atoi(m[5]), atoi(m[6]), 0)
	lat, lon := tenthMinutes(m[10], m[11], m[12], m[13], m[14], m[15])
	if !ok || lat > 90 || lon < -180 || lon > 180 {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Avianca", Event: "POSRPT", Origin: m[2], Dest: m[3], Reg: m[4], ETA: hhmm(m[24]),
		Info: map[string]string{"waypoint": m[7], "next": m[8], "fob": m[23], "tas": m[17]}}
	o.Callsign, o.Flight = opsFlight("AVA", m[1])
	alt := float64(atoi(m[16]))
	temp := float64(atoi(m[19]))
	if m[18] == "-" {
		temp = -temp
	}
	dir, kt := float64(atoi(m[22])), float64(atoi(m[20]))
	if m[21] == "-" {
		dir = 360 - dir
	}
	w := &Weather{TempC: &temp}
	if dir >= 0 && dir <= 360 && kt <= 250 {
		w.WindDirDeg, w.WindKt = &dir, &kt
	}
	mach := atof(m[9])
	o.Info["mach"] = strconv.FormatFloat(mach, 'f', -1, 64)
	s := fmt.Sprintf("Avianca %s position report %s %s, %s", o.name(), route(o.Origin, o.Dest), latlon(lat, lon), flightLevel(alt))
	if mach > 0.4 {
		s += fmt.Sprintf(", M%.3f", mach)
	}
	return opsParsed{rep: o, pos: &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: secs, Basis: BasisDay}, wx: w, summary: s, tail: m[4]}, true
}

// The report on label 99:
//
//	EMR05/06:31:44,N39537W005182,31541,0.804,295,-17,461
//	082,085,081,084
//	8.6,32.8,9.0,33.4
//	...
//
// The first line is read: the time to the second, the position, the altitude in feet, the Mach number and, last, the true
// airspeed (0.804 at 31,541 ft is 461 kt). The 295 and -17 are not understood, and the lines after are engine and system
// values, which are left as sent.
var emrRe = regexp.MustCompile(`^EMR(\d{2})/(\d{2}):(\d{2}):(\d{2}),([NS])(\d{2})(\d{3})([EW])(\d{3})(\d{3}),(\d+),(\d*\.\d+),(\d+),[-+]?\d+,(\d+)`)

func parseEMR(text string) (opsParsed, bool) {
	m := emrRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return opsParsed{}, false
	}
	secs, ok := daySecs(atoi(m[2]), atoi(m[3]), atoi(m[4]))
	lat, lon := tenthMinutes(m[5], m[6], m[7], m[8], m[9], m[10])
	if !ok || lat > 90 || lon > 180 {
		return opsParsed{}, false
	}
	alt := float64(atoi(m[11]))
	o := OpsReport{Airline: "Avianca", Event: "EMR", Info: map[string]string{"mach": m[12], "tas": m[14]}}
	s := fmt.Sprintf("Avianca position report %s, %s", latlon(lat, lon), flightLevel(alt))
	if mach := atof(m[12]); mach > 0.4 {
		s += fmt.Sprintf(", M%.3f", mach)
	}
	return opsParsed{rep: o, pos: &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: secs, Basis: BasisDay}, summary: s}, true
}

// The takeoff report on label 24:
//
//	TKO01    AVA4907/28281720/LEMD/SKBO
//	/DT 1656/TO 1706/TOF  523
//	/DEP 3/FLAP  5/TPR ***TKO****
//	TOF 54500KG
//
// The flight, its date, the day and time of the report (the first two digits are the flight's date and the next two the
// day it was sent), the route, the time it left the gate (DT) and the time it took off (TO), then two numbers called TOF:
// the first about 500 kg (probably the fuel burnt taxiing: it was ten minutes), the last the fuel at takeoff, written as
// 54500 KG or in tonnes (57.0 KG), and the flap setting. DEP is kept as sent. Read from five reports; the takeoff time is
// what the aircraft's own track shows.
var tkoRe = regexp.MustCompile(`^TKO\d{2} ([A-Z]{3})(\d{3,4})/(\d{2})(\d{2})(\d{4})/([A-Z]{4})/([A-Z]{4}) /DT (\d{4})/TO (\d{4})/TOF (\d+) /DEP (\w+)/FLAP (\d+)/TPR \S+ TOF ?(\d+(?:\.\d+)?)`)

func parseTKO(text string) (opsParsed, bool) {
	m := tkoRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil || hhmm(m[8]) == "" || hhmm(m[9]) == "" {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Avianca", Event: "TKO", Origin: m[6], Dest: m[7], Out: hhmm(m[8]), Off: hhmm(m[9]),
		Info: map[string]string{"day": m[3], "dep": m[11], "flaps": m[12], "taxi_fuel": m[10]}}
	o.Callsign, o.Flight = opsFlight(m[1], m[2])
	fuel := atof(m[13])
	if fuel < 1000 {
		fuel *= 1000 // written in tonnes
	}
	o.Info["takeoff_fuel_kg"] = strconv.Itoa(int(fuel + .5))
	s := fmt.Sprintf("Avianca %s takeoff report %s: %s kg of fuel, flaps %s", o.name(), route(o.Origin, o.Dest), groupThousands(int(fuel+.5)), m[12])
	return opsParsed{rep: o, summary: s}, true
}

// The position report that starts EB (EB0032AA_ with one position, EB0031AA_ with a position a minute for several minutes):
//
//	EB0032AA_ N781AV LEMDSKBO 36
//	 40307-0050761720 24824-301339016010/9W003004
//
// The tail, the route and a count; then for each position the latitude and the longitude in thousandths of a degree, the
// time (hhmm), the altitude in feet, the temperature in whole degrees (a space for a plus), then a digit and the wind
// direction and speed in three digits each (339 degrees, 16 kt there: the POSRPT five minutes later said 334 and 21).
// The twelve characters after that are not understood (the last line of a long message is often cut short there). The newest
// line is the message's position and the rest are in its text. Read from 36 messages; the positions lie on the track and the altitudes match the neighbouring reports.
var (
	ebHeadRe = regexp.MustCompile(`^EB\d{4}[A-Z]{2}_ ([A-Z0-9-]+) ([A-Z]{4})([A-Z]{4})\s*\d+\s*(?:\n|$)`)
	ebLineRe = regexp.MustCompile(`^\s*(-?\d{5})-(\d{6})(\d{4}) (\d{5})([-+ ]\d{2})\d(\d{3})(\d{3})\S{0,12}\s*$`)
)

type ebFix struct {
	lat, lon, alt, temp, dir, kt float64
	secs                         float64
}

func parseEB(text string) (opsParsed, bool) {
	t := strings.ReplaceAll(strings.TrimSpace(text), "\r", "")
	h := ebHeadRe.FindStringSubmatch(t)
	if h == nil {
		return opsParsed{}, false
	}
	var fixes []ebFix
	for _, line := range strings.Split(t, "\n")[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := ebLineRe.FindStringSubmatch(line)
		if m == nil {
			return opsParsed{}, false // a line that is not a position: something else
		}
		secs, ok := daySecs(atoi(m[3][:2]), atoi(m[3][2:]), 0)
		f := ebFix{lat: atof(m[1]) / 1000, lon: -atof(m[2]) / 1000, alt: atof(m[4]), temp: atof(strings.Replace(m[5], " ", "+", 1)),
			dir: atof(m[6]), kt: atof(m[7]), secs: secs}
		if !ok || abs(f.lat) > 90 || f.lon < -180 {
			return opsParsed{}, false
		}
		fixes = append(fixes, f)
	}
	if len(fixes) == 0 {
		return opsParsed{}, false
	}
	n := fixes[len(fixes)-1]
	o := OpsReport{Airline: "Avianca", Event: "EB", Origin: h[2], Dest: h[3], Reg: h[1], Info: map[string]string{"positions": strconv.Itoa(len(fixes))}}
	w := &Weather{TempC: &n.temp}
	if n.dir <= 360 && n.kt <= 250 {
		w.WindDirDeg, w.WindKt = &n.dir, &n.kt
	}
	s := fmt.Sprintf("Avianca position report %s %s, %s", route(o.Origin, o.Dest), latlon(n.lat, n.lon), flightLevel(n.alt))
	var extra []string
	if len(fixes) > 1 {
		s += fmt.Sprintf(" (the newest of %d positions)", len(fixes))
		for _, f := range fixes {
			extra = append(extra, fmt.Sprintf("%02d:%02dZ %s, %s ft", int(f.secs)/3600, int(f.secs)%3600/60, latlon(f.lat, f.lon), groupThousands(int(f.alt))))
		}
	}
	return opsParsed{rep: o, pos: &Pos{Lat: n.lat, Lon: n.lon, AltFt: &n.alt, Secs: n.secs, Basis: BasisDay}, wx: w, summary: s, extra: extra, tail: h[1]}, true
}
