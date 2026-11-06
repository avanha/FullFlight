package decoder

import (
	"fmt"
	"regexp"
	"strings"
)

// Delta's dispatch messages to and from the cockpit, on label RA (to the aircraft) and 25 (from it), each starting with a two-digit
// class ("40", "42", "50"). Read from N501DN's traffic. The weights are in thousands of pounds, and the day is the flight's date:
//
//	50 PRE-PUSHBACK MSG        FLT 0782/02NOV, A/C 3501 LAX-ATL RLS 1, fuel total and taxi, the open MEL items, the runways
//	50 ACARS FUEL SVC RCD      the fuel the fueler put in (gallons, and pounds in all), tank by tank
//	50 ACARS WDR UPDATE        the weight and data release: people on board, cargo, fuel, weights, take-off runway, MEL items
//	42 WIDE WDR 782/02 1602Z   the same with the runway data set beside it in a second column
//	40 FLT NBR DAL87 RLS 1     the dispatch release: weights, passengers, fuel plan, the route, the ETOPS alternates
//
// A long message arrives in blocks, and the first is cut anywhere, so each value is read where it is found and none is needed.
// What the crew's names and numbers (the "RLS VERIFICATION" message) and the fueler's name say is not read.
var (
	deltaHeadRe = regexp.MustCompile(`^(?:\d{2} )?(PRE-PUSHBACK MSG|ACARS FUEL SVC RCD|ACARS WDR UPDATE(?:-TOPR)?|WIDE WDR \d+/\d+ \d{4}Z|FLT NBR [A-Z]{3}\d+ +RLS \d+)`)
	deltaFltRe  = regexp.MustCompile(`FLT 0*(\d+)/(\d{1,2})([A-Z]{3})`)
	deltaRteRe  = regexp.MustCompile(`(?:SHIP|A/C) (\d+) ([A-Z]{3})-([A-Z]{3})(?: RLS (\d+))?`)
	deltaMELRe  = regexp.MustCompile(`\b[A-Z]\d{2}-\d{2}-\d{2}[A-Z]?\b`)
	deltaRwyRe  = regexp.MustCompile(`\*(\d{2}[LRC]?)(?:-([A-Z0-9]+))?\* +(\d+)FT +(\d+)(TW|HW) +(\d+)XW`)
)

// pounds writes a weight sent in thousands of pounds.
func pounds(k string) string { return groupThousands(int(atof(k)*1000+.5)) + " lb" }

func parseDelta(text string) (opsParsed, bool) {
	t := strings.NewReplacer("\r", "", "\x07", "").Replace(strings.TrimSpace(text))
	first, _, _ := strings.Cut(t, "\n")
	h := deltaHeadRe.FindStringSubmatch(strings.TrimSpace(first))
	if h == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Delta", Info: map[string]string{}}
	var lines []string
	switch {
	case strings.HasPrefix(h[1], "FLT NBR"):
		o.Event = "dispatch release"
		lines = deltaRelease(t, &o)
	default:
		if f := deltaFltRe.FindStringSubmatch(t); f != nil {
			o.Callsign, o.Flight = opsFlight("DAL", f[1])
			o.Info["day"] = fmt.Sprintf("%02d", atoi(f[2]))
			o.Info["month"] = f[3]
		} else if w := regexp.MustCompile(`WIDE WDR 0*(\d+)/(\d+)`).FindStringSubmatch(h[1]); w != nil {
			o.Callsign, o.Flight = opsFlight("DAL", w[1])
			o.Info["day"] = fmt.Sprintf("%02d", atoi(w[2]))
		}
		if r := deltaRteRe.FindStringSubmatch(t); r != nil {
			o.Info["ship"], o.Origin, o.Dest = r[1], icaoOf(r[2]), icaoOf(r[3])
			if r[4] != "" {
				o.Info["release"] = r[4]
			}
		}
		switch {
		case strings.HasPrefix(h[1], "PRE-PUSHBACK"):
			o.Event = "pre-pushback"
			lines = deltaPushback(t, &o)
		case strings.HasPrefix(h[1], "ACARS FUEL"):
			o.Event = "fuel"
			lines = deltaFuel(t, &o)
		default:
			o.Event = "WDR"
			lines = deltaWDR(t, &o)
		}
	}
	var mel []string
	if i := strings.Index(t, "MEL"); i >= 0 {
		mel = deltaMELRe.FindAllString(t[i:], -1)
	}
	if len(mel) > 0 {
		o.Info["mel"] = strings.Join(mel, " ")
		lines = append(lines, fmt.Sprintf("Open deferred defects (MEL): %s", strings.Join(mel, ", ")))
	}
	what := map[string]string{"dispatch release": "dispatch release", "pre-pushback": "pre-pushback message", "fuel": "fuel received", "WDR": "weight and data release"}[o.Event]
	s := strings.TrimSpace(fmt.Sprintf("Delta %s %s %s", strings.TrimPrefix(o.Flight, "DL"), what, route(o.Origin, o.Dest)))
	if sum := deltaSummary(o); sum != "" {
		s += ": " + sum
	}
	return opsParsed{rep: o, summary: s, extra: lines, uplink: true, tail: o.Reg}, true
}

// deltaSummary is the few values that say most about a message, for its one line.
func deltaSummary(o OpsReport) string {
	var p []string
	if v := o.Info["pax"]; v != "" {
		p = append(p, v+" passengers")
	}
	if v := o.Info["on_board"]; v != "" {
		p = append(p, v+" on board")
	}
	if v := o.Info["tow"]; v != "" {
		p = append(p, "take-off "+pounds(v))
	}
	if v := o.Info["fuel_lb"]; v != "" {
		p = append(p, "fuel "+v+" lb")
	} else if v := o.Info["fuel"]; v != "" {
		p = append(p, "fuel "+pounds(v))
	}
	if o.Runway != "" {
		p = append(p, "runway "+o.Runway)
	}
	if v := o.Info["mel"]; v != "" {
		p = append(p, fmt.Sprintf("%d open MEL items", len(strings.Fields(v))))
	}
	return strings.Join(p, ", ")
}

func deltaPushback(t string, o *OpsReport) (lines []string) {
	if strings.Contains(t, "FUEL CLOSEOUT PENDING") {
		lines = append(lines, "Fuel closeout pending: not to push back without a fuelling record")
	} else if m := regexp.MustCompile(`TOTAL +(\d+\.\d) +TAXI +(\d+\.\d)`).FindStringSubmatch(t); m != nil && atof(m[1]) > 0 {
		o.Info["fuel"], o.Info["taxi_fuel"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Fuel %s, of it %s for taxi", pounds(m[1]), pounds(m[2])))
	}
	if i := strings.Index(t, "WDR RWYS:"); i >= 0 {
		var rwys []string
		for _, l := range strings.Split(t[i+len("WDR RWYS:"):], "\n") {
			if f := strings.Fields(l); len(f) == 1 && regexp.MustCompile(`^\d{2}[LRC]?(-[A-Z0-9]+)?$`).MatchString(f[0]) {
				rwys = append(rwys, f[0])
			} else if len(rwys) > 0 {
				break
			}
		}
		if len(rwys) > 0 {
			o.Info["runways"] = strings.Join(rwys, " ")
			lines = append(lines, "Runways the weight data was made for: "+strings.Join(rwys, ", "))
		}
	}
	return lines
}

func deltaFuel(t string, o *OpsReport) (lines []string) {
	if m := regexp.MustCompile(`FUEL BOARDED IN GALLONS - +(\d+)`).FindStringSubmatch(t); m != nil {
		o.Info["fuel_gal"] = fmt.Sprint(atoi(m[1]))
		lines = append(lines, fmt.Sprintf("Fuel boarded: %s gallons", groupThousands(atoi(m[1]))))
	}
	if m := regexp.MustCompile(`TOTAL FUEL OUT IN POUNDS - +(\d+)`).FindStringSubmatch(t); m != nil {
		o.Info["fuel_lb"] = groupThousands(atoi(m[1]))
		lines = append(lines, fmt.Sprintf("Fuel on board after: %s lb", groupThousands(atoi(m[1]))))
	}
	for _, m := range regexp.MustCompile(`(?m)^(LM|RM|CA) +(\d{6}) +(\d{6}) +(\d{6})`).FindAllStringSubmatch(t, -1) {
		lines = append(lines, fmt.Sprintf("Tank %s: %s lb before, %s lb added, %s lb after", m[1], groupThousands(atoi(m[2])), groupThousands(atoi(m[3])), groupThousands(atoi(m[4]))))
	}
	if m := regexp.MustCompile(`EFSR DATE/TIME (\d{2})([A-Z]{3}) (\d{4})Z`).FindStringSubmatch(t); m != nil {
		lines = append([]string{fmt.Sprintf("Fuelling record made %s %s at %sZ", m[1], m[2], m[3])}, lines...)
	}
	return lines
}

func deltaWDR(t string, o *OpsReport) (lines []string) {
	if m := regexp.MustCompile(`SOB/(\d+)`).FindStringSubmatch(t); m != nil {
		o.Info["on_board"] = m[1]
		lines = append(lines, m[1]+" people on board")
	}
	if m := regexp.MustCompile(`CARGO ([\d./]+?)\.?(?:\s|$)`).FindStringSubmatch(t); m != nil {
		o.Info["cargo"] = m[1]
		lines = append(lines, "Cargo, as sent: "+m[1])
	}
	if m := regexp.MustCompile(`FUEL TOTAL (\d+\.\d) +TAXI (\d+\.\d)`).FindStringSubmatch(t); m != nil {
		o.Info["fuel"], o.Info["taxi_fuel"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Fuel %s, of it %s for taxi", pounds(m[1]), pounds(m[2])))
	}
	if m := regexp.MustCompile(`RMWT +(\d+\.\d) +ZFWT +(\d+\.\d) +ZFWCG +(\d+\.\d)`).FindStringSubmatch(t); m != nil {
		o.Info["ramp"], o.Info["zfw"], o.Info["zfwcg"] = m[1], m[2], m[3]
		lines = append(lines, fmt.Sprintf("Ramp weight %s, zero fuel weight %s (centre of gravity %s%%)", pounds(m[1]), pounds(m[2]), m[3]))
	}
	if m := regexp.MustCompile(`TOWT +(\d+\.\d) +TOCG +(\d+\.\d)`).FindStringSubmatch(t); m != nil {
		o.Info["tow"], o.Info["tocg"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Take-off weight %s (centre of gravity %s%%)", pounds(m[1]), m[2]))
	}
	if m := regexp.MustCompile(`WX (\d{3})(\d{2})KT +(M?\d+)C A(\d{2})\.(\d{2})`).FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Weather at take-off: wind %s° at %d kt, %s°C, altimeter %s.%s", m[1], atoi(m[2]), strings.Replace(m[3], "M", "-", 1), m[4], m[5]))
	}
	seen := map[string]bool{}
	for _, m := range deltaRwyRe.FindAllStringSubmatch(t, -1) {
		name := m[1]
		if m[2] != "" {
			name += "-" + m[2]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if o.Runway == "" {
			o.Runway = name
		}
		wind := "tail"
		if m[5] == "HW" {
			wind = "head"
		}
		lines = append(lines, fmt.Sprintf("Take-off runway %s, %s ft, %d kt %swind, %d kt crosswind", name, groupThousands(atoi(m[3])), atoi(m[4]), wind, atoi(m[6])))
	}
	return lines
}

func deltaRelease(t string, o *OpsReport) (lines []string) {
	if m := regexp.MustCompile(`FLT NBR ([A-Z]{3})(\d+) +RLS (\d+)`).FindStringSubmatch(t); m != nil {
		o.Callsign, o.Flight = opsFlight(m[1], m[2])
		o.Info["release"] = m[3]
	}
	if m := regexp.MustCompile(`([A-Z]{4})-([A-Z]{4})(?: +ALT ([A-Z]{4}))?`).FindStringSubmatch(t); m != nil {
		o.Origin, o.Dest = m[1], m[2]
		if m[3] != "" {
			o.Info["alternate"] = m[3]
			lines = append(lines, "Alternate airport "+m[3])
		}
	}
	// the last line names the flight's date, the route again, and the aircraft
	if m := regexp.MustCompile(`[A-Z]{3}\d+/(\d{2})([A-Z]{3}) [A-Z]{4}-[A-Z]{4} RLS \d+ ([A-Z0-9-]+) \(ACARS FLT NO (\d+)\)`).FindStringSubmatch(t); m != nil {
		o.Info["day"], o.Info["month"], o.Reg = m[1], m[2], m[3]
	}
	num := func(key, re string, f func(string) string) {
		if m := regexp.MustCompile(re).FindStringSubmatch(t); m != nil {
			o.Info[key] = m[1]
			lines = append(lines, f(m[1]))
		}
	}
	num("pax", `PAX NBR (\d+)`, func(v string) string { return v + " passengers" })
	num("zfw", `ZFW +(\d+\.\d)`, func(v string) string { return "Zero fuel weight " + pounds(v) })
	num("tow", `\bTOW +(\d+\.\d)`, func(v string) string { return "Take-off weight " + pounds(v) })
	num("lw", `\bLW +(\d+\.\d)`, func(v string) string { return "Landing weight " + pounds(v) })
	num("block", `BLOCK +(\d+\.\d)`, func(v string) string { return "Block fuel " + pounds(v) })
	if m := regexp.MustCompile(`\bTRIP +(\d+\.\d) +(\d+:\d+)`).FindStringSubmatch(t); m != nil {
		o.Info["trip_fuel"], o.Info["trip_time"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Trip fuel %s for %s of flying", pounds(m[1]), m[2]))
	}
	num("cruise", `CRZ FL(\d+)`, func(v string) string { return "Cruise FL" + v })
	num("dist_nm", `DIST (\d+) NM`, func(v string) string { return v + " nm planned" })
	if i := strings.Index(t, "FLIGHT PLAN\n"); i >= 0 {
		rest := t[i+len("FLIGHT PLAN\n"):]
		if j := strings.Index(rest, "\n\n"); j >= 0 {
			rest = rest[:j]
		}
		o.Info["route"] = strings.Join(strings.Fields(rest), " ")
		lines = append(lines, "Route: "+o.Info["route"])
	}
	if i := strings.Index(t, "ETOPS\n"); i >= 0 {
		rest := t[i+len("ETOPS\n"):]
		if j := strings.Index(rest, "\n\n"); j >= 0 {
			rest = rest[:j]
		}
		var alts []string
		for _, w := range strings.Fields(rest) {
			if icaoOK(w) {
				alts = append(alts, w)
			}
		}
		if len(alts) > 0 {
			o.Info["etops"] = strings.Join(alts, " ")
			lines = append(lines, "ETOPS airports: "+strings.Join(alts, ", "))
		}
	}
	return lines
}
