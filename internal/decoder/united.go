package decoder

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// United's dispatch messages to the cockpit, on label RA (to the aircraft). Each starts with an address and a title,
// and most with the flight, its date and route and the time it was sent. Read from United's traffic of one week:
//
//	QUNDCULUA~1TAKEOFF DATA     the take-off performance: runway, conditions, flaps, thrust, V speeds, MEL items
//	QUNDCULUA~1HOWGOZIT         the flight plan against the clock: cost index, a waypoint table, planned arrival
//	QUNDCULUA~1DISPATCH RELEASE the release: flight, aircraft, minimum fuel, alternates
//	QUNDCULUA~1FINAL WEIGHTS    the load: take-off weight, zero fuel weight, centre of gravity, people on board
//	QUNDCULUA~1GATE ASSIGN      the gate, the frequencies there, the turn-round and the next flight
//	QUNDCULUA~1TURB SIGMET      the aircraft's flight enters a turbulence area
//	QUNDCULUA~1FLIGHT STATUS, CABIN FAULT RPT   named and the flight read
//
// The crew list, the release verification and the messages between pilot and dispatcher carry names and employee numbers, or are
// free text: they are not read. A long message arrives in blocks and the first is cut anywhere, so each value is read where found.
var (
	unitedHeadRe = regexp.MustCompile(`^(?:\.?[A-Z0-9]{7,9}~\d)?\s*(TAKEOFF DATA|HOWGOZIT|DISPATCH RELEASE|GATE ASSIGN|FINAL WEIGHTS|FLIGHT STATUS|CABIN FAULT RPT|TURB SIGMET)\b`)
	unitedFltRe  = regexp.MustCompile(`\bUA0*(\d+)/(\d\d) ([A-Z]{4}) ([A-Z]{4})`)
	unitedTurbRe = regexp.MustCompile(`\bUAL0*(\d+)-(\d\d) ([A-Z]{4}) ([A-Z]{4})`)
	unitedHowRe  = regexp.MustCompile(`HOWGOZIT 0*(\d+)-(\d\d) ([A-Z]{3})`)
	unitedSent   = regexp.MustCompile(`SENT: *(\d\d:\d\d(?::\d\d)?)Z?`)
)

func parseUnited(text string) (opsParsed, bool) {
	t := html.UnescapeString(strings.NewReplacer("\r", "", "\x07", "", "\t", "").Replace(strings.TrimSpace(text)))
	// lines with their indentation trimmed: a message sent the other way round arrives indented
	var ls []string
	for _, l := range strings.Split(t, "\n") {
		ls = append(ls, strings.TrimRight(strings.TrimSpace(l), " "))
	}
	t = strings.Join(ls, "\n")
	h := unitedHeadRe.FindStringSubmatch(t)
	if h == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "United", Info: map[string]string{}}
	switch {
	case h[1] == "HOWGOZIT":
		if m := unitedHowRe.FindStringSubmatch(t); m != nil {
			o.Callsign, o.Flight = opsFlight("UAL", m[1])
			o.Info["day"], o.Origin = m[2], icaoOf(m[3])
		}
	case h[1] == "TURB SIGMET":
		if m := unitedTurbRe.FindStringSubmatch(t); m != nil {
			o.Callsign, o.Flight = opsFlight("UAL", m[1])
			o.Info["day"], o.Origin, o.Dest = m[2], m[3], m[4]
		}
	default:
		if m := unitedFltRe.FindStringSubmatch(t); m != nil {
			o.Callsign, o.Flight = opsFlight("UAL", m[1])
			o.Info["day"], o.Origin, o.Dest = m[2], m[3], m[4]
		}
	}
	if m := unitedSent.FindStringSubmatch(t); m != nil {
		o.Info["sent"] = m[1]
	}
	var lines []string
	switch h[1] {
	case "TAKEOFF DATA":
		o.Event = "take-off data"
		lines = unitedTakeoff(t, &o)
	case "HOWGOZIT":
		o.Event = "howgozit"
		lines = unitedHowgozit(t, &o)
	case "DISPATCH RELEASE":
		o.Event = "dispatch release"
		lines = unitedRelease(t, &o)
	case "FINAL WEIGHTS":
		o.Event = "final weights"
		lines = unitedWeights(t, &o)
	case "GATE ASSIGN":
		o.Event = "gate assignment"
		lines = unitedGate(t, &o)
	case "TURB SIGMET":
		o.Event = "turbulence SIGMET"
		lines = unitedTurb(t, &o)
	case "FLIGHT STATUS":
		o.Event = "flight status"
		if strings.Contains(t, "NOT AFFECTED BY\nFLOW CONTROL") || strings.Contains(t, "NOT AFFECTED BY FLOW CONTROL") {
			o.Info["flow_control"] = "no"
			lines = append(lines, "The flight is not affected by flow control")
		}
	case "CABIN FAULT RPT":
		o.Event = "cabin fault report"
		if strings.Contains(t, "NEW MRD IS REQUIRED") {
			o.Info["mrd"] = "required"
			lines = append(lines, "Cabin faults reported: a new maintenance release is needed before the next flight")
		}
	}
	var mel []string
	if i := strings.Index(t, "MEL/CDL:"); i >= 0 {
		mel = unitedItems(t[i+len("MEL/CDL:"):])
		if len(mel) > 0 {
			o.Info["mel"] = strings.Join(mel, "; ")
			lines = append(lines, "Open deferred defects (MEL/CDL): "+strings.Join(mel, "; "))
		}
	}
	s := "United"
	if o.Flight != "" {
		s += " " + strings.TrimPrefix(o.Flight, "UA")
	}
	s += " " + o.Event
	if r := route(o.Origin, o.Dest); r != "" {
		s += " " + r
	}
	if sum := unitedSummary(o); sum != "" {
		s += ": " + sum
	}
	return opsParsed{rep: o, summary: s, extra: lines, uplink: true, tail: o.Reg}, true
}

// unitedItems reads the lines of a list that ends at a line of asterisks or a blank line: an item that wraps on to the next
// line (a line that starts in lower indentation) is put back together. Items are told apart by starting with a digit code.
func unitedItems(t string) []string {
	var items []string
	for _, l := range strings.Split(strings.TrimLeft(t, "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "***") {
			break
		}
		if regexp.MustCompile(`^\d{3,4}\b`).MatchString(l) || len(items) == 0 {
			items = append(items, l)
		} else {
			items[len(items)-1] += " " + l
		}
	}
	return items
}

func unitedSummary(o OpsReport) string {
	var p []string
	for _, k := range []struct{ key, label string }{{"runway", "runway "}, {"vspeeds", "V1/VR/V2 "}, {"tog", "take-off weight "}, {"min_fuel", "minimum take-off fuel "}, {"souls", ""}, {"severity", ""}, {"gate", "gate "}, {"arrival", "arrival "}} {
		if v := o.Info[k.key]; v != "" {
			switch k.key {
			case "souls":
				v += " on board"
			case "severity":
				v += " turbulence"
			}
			p = append(p, k.label+v)
		}
	}
	if v := o.Info["mel"]; v != "" {
		p = append(p, fmt.Sprintf("%d open MEL/CDL items", len(strings.Split(v, "; "))))
	}
	return strings.Join(p, ", ")
}

var (
	unitedTORe    = regexp.MustCompile(`T/O ([A-Z]{3,4}) (\d{2}[LRC]?)(?:/([A-Z0-9]+))?`)
	unitedTypeRe  = regexp.MustCompile(`(?m)^([AB]\d{3}-\w+) +(\S.*)$`)
	unitedTempRe  = regexp.MustCompile(`TEMP +(-?\d+)C +ALT +(\d\d\.\d\d)`)
	unitedWindRe  = regexp.MustCompile(`WIND +(\d{3})/(\d+) MAG`)
	unitedSpdRe   = regexp.MustCompile(`(?m)^(\d+\.\d) +(?:\d+KT)? *(\d{2,3}) (\d{2,3}) (\d{2,3})$`)
	unitedMaxRe   = regexp.MustCompile(`(?m)^(\d+\.\d) +(\d+\.\d) +(\d{2,3}) (\d{2,3}) (\d{2,3})$`)
	unitedThrRe   = regexp.MustCompile(`THR RED (\d+) \((\d+) AFE\)`)
	unitedAccRe   = regexp.MustCompile(`ACC ALT (\d+) \((\d+) AFE\)`)
	unitedAssmdRe = regexp.MustCompile(`ASSMD WT: +(\d+\.\d)\s+ASSMD TMP: +(-?\d+)C`)
)

func unitedTakeoff(t string, o *OpsReport) (lines []string) {
	if m := unitedTORe.FindStringSubmatch(t); m != nil {
		o.Origin, o.Runway = icaoOf(m[1]), m[2]
		o.Info["runway"] = m[2]
		l := fmt.Sprintf("Take-off from %s, runway %s", m[1], m[2])
		if m[3] != "" {
			l += "/" + m[3]
		}
		lines = append(lines, l)
	}
	if m := unitedTypeRe.FindStringSubmatch(t); m != nil {
		o.Info["type"] = m[1]
		lines = append(lines, "Aircraft "+m[1]+", engines "+m[2])
	}
	if m := unitedTempRe.FindStringSubmatch(t); m != nil {
		o.Info["temp_c"], o.Info["altimeter"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Temperature %s°C, altimeter %s", m[1], m[2]))
	}
	if m := unitedWindRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Wind %s° at %s kt (magnetic)", m[1], m[2]))
	}
	for _, c := range []string{"DRY", "WET", "CONTAMINATED"} {
		if regexp.MustCompile(`(?m)^` + c + `$`).MatchString(t) {
			o.Info["surface"] = strings.ToLower(c)
			lines = append(lines, "Runway "+strings.ToLower(c))
			break
		}
	}
	var cfg []string
	if m := regexp.MustCompile(`\*FLAPS (\w+)\*`).FindStringSubmatch(t); m != nil {
		o.Info["flaps"] = m[1]
		cfg = append(cfg, "flaps "+m[1])
	}
	if m := regexp.MustCompile(`\*BLEEDS (\w+)\*`).FindStringSubmatch(t); m != nil {
		cfg = append(cfg, "bleeds "+strings.ToLower(m[1]))
	}
	if m := regexp.MustCompile(`\*ANTI-ICE (\w+)\*`).FindStringSubmatch(t); m != nil {
		cfg = append(cfg, "anti-ice "+strings.ToLower(m[1]))
	}
	if len(cfg) > 0 {
		lines = append(lines, "Configuration: "+strings.Join(cfg, ", "))
	}
	if m := unitedAssmdRe.FindStringSubmatch(t); m != nil {
		o.Info["assumed_wt"], o.Info["assumed_temp"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Reduced thrust: assumed weight %s, assumed temperature %s°C", pounds(m[1]), m[2]))
	}
	// the first table of speeds is the reduced thrust one, the table after "MAX" the full thrust one
	cut := strings.Index(t, "MAX EPR")
	if cut < 0 {
		cut = strings.Index(t, "MAX N1")
	}
	before, after := t, ""
	if cut >= 0 {
		before, after = t[:cut], t[cut:]
	}
	if m := unitedSpdRe.FindStringSubmatch(before); m != nil {
		o.Info["vspeeds"] = m[2] + "/" + m[3] + "/" + m[4]
		lines = append(lines, fmt.Sprintf("Reduced thrust: N1 %s, V1 %s, VR %s, V2 %s", m[1], m[2], m[3], m[4]))
	}
	if m := unitedMaxRe.FindStringSubmatch(after); m != nil {
		o.Info["full_thrust_wt"] = pounds(m[2])
		lines = append(lines, fmt.Sprintf("Full thrust at %s: N1 %s, V1 %s, VR %s, V2 %s", pounds(m[2]), m[1], m[3], m[4], m[5]))
	}
	if m := unitedThrRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Thrust reduction at %s ft (%s ft above the field)", m[1], m[2]))
	}
	if m := unitedAccRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Acceleration altitude %s ft (%s ft above the field)", m[1], m[2]))
	}
	if m := regexp.MustCompile(`MTOG +(\d+\.\d) +VR MAX +(\d+)`).FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Maximum take-off weight %s, VR limit %s", pounds(m[1]), m[2]))
	}
	return lines
}

var (
	unitedHowCIRe  = regexp.MustCompile(`CI: *(\d+) +RLS: *(\d+)`)
	unitedHowWptRe = regexp.MustCompile(`(?m)^(\S+) +(\d{4}) (\d\d) +(\d+)[A-Z-]?$`)
	unitedHowArrRe = regexp.MustCompile(`(?m)^([A-Z]{3}) (\d{4})/(\d{4}) +(\d+)$`)
	unitedHowSchRe = regexp.MustCompile(`--SCHEDULED ARRIVAL--\s+(\d{4})/(\d{4}) \((\d\d)\.(\d\d)([LE])\)`)
)

func unitedHowgozit(t string, o *OpsReport) (lines []string) {
	if m := unitedHowCIRe.FindStringSubmatch(t); m != nil {
		o.Info["cost_index"], o.Info["release"] = m[1], m[2]
		lines = append(lines, fmt.Sprintf("Cost index %s, release %s", m[1], m[2]))
	}
	if n := len(unitedHowWptRe.FindAllString(t, -1)); n > 0 {
		o.Info["waypoints"] = fmt.Sprint(n)
		lines = append(lines, fmt.Sprintf("%d waypoints, each with its planned time, flight level (hundreds of feet) and fuel as sent", n))
	}
	if m := unitedHowArrRe.FindStringSubmatch(t); m != nil {
		o.Dest = icaoOf(m[1])
		o.Info["arrival"] = m[2][:2] + ":" + m[2][2:] + "Z"
		lines = append(lines, fmt.Sprintf("Planned arrival at %s %s Z / %s Z", m[1], m[2], m[3]))
	}
	if m := unitedHowSchRe.FindStringSubmatch(t); m != nil {
		by := "late"
		if m[5] == "E" {
			by = "early"
		}
		lines = append(lines, fmt.Sprintf("Scheduled arrival %sZ / %sZ: the plan is %dh%02dm %s", m[1], m[2], atoi(m[3]), atoi(m[4]), by))
	}
	return lines
}

var (
	unitedRelTailRe = regexp.MustCompile(`(?m)^(N\d+[A-Z]{0,2})/(\d+) (\w{3,4})$`)
	unitedRelDayRe  = regexp.MustCompile(`UAL\d+\(\d+\) - (\d\d)([A-Z]{3})(\d\d)`)
	unitedRelFuelRe = regexp.MustCompile(`MIN T/O FUEL: *(\d+)`)
	unitedRelAltnRe = regexp.MustCompile(`(DEPARTURE|DESTINATION) ALTN: *(\S+)`)
	unitedRelStdRe  = regexp.MustCompile(`ST DEP (\d{4})`)
)

func unitedRelease(t string, o *OpsReport) (lines []string) {
	if m := unitedRelTailRe.FindStringSubmatch(t); m != nil {
		o.Reg = m[1]
		o.Info["fleet"], o.Info["type"] = m[2], m[3]
		lines = append(lines, fmt.Sprintf("Aircraft %s (fleet number %s), type %s", m[1], m[2], m[3]))
	}
	if m := unitedRelDayRe.FindStringSubmatch(t); m != nil {
		o.Info["day"], o.Info["month"] = m[1], m[2]
	}
	if m := unitedRelFuelRe.FindStringSubmatch(t); m != nil {
		o.Info["min_fuel"] = groupThousands(atoi(m[1])) + " lb"
		lines = append(lines, "Minimum take-off fuel "+o.Info["min_fuel"])
	}
	for _, m := range unitedRelAltnRe.FindAllStringSubmatch(t, -1) {
		if m[2] != "N/A" {
			lines = append(lines, strings.ToLower(m[1][:1])+m[1][1:]+" alternate "+m[2])
		}
	}
	if m := unitedRelStdRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, "Scheduled departure "+m[1]+"Z")
	}
	return lines
}

var (
	unitedWtTOGRe = regexp.MustCompile(`TOG +(\d+) +SEQ:(\d+)`)
	unitedWtChgRe = regexp.MustCompile(`TOW CHG +([+-]) *(\d+)`)
	unitedWtZFWRe = regexp.MustCompile(`ZFW +(\d+)`)
	unitedWtCGRe  = regexp.MustCompile(`(?m)^CG +(\d+\.\d)`)
	unitedWtSOBRe = regexp.MustCompile(`SOB (\d+)`)
	unitedWtPaxRe = regexp.MustCompile(`PSGRS +(\d+)/(\d+) +-(\d+)-`)
	unitedWtCrwRe = regexp.MustCompile(`CREW +(\d+)/(\d+)`)
)

func unitedWeights(t string, o *OpsReport) (lines []string) {
	if m := unitedWtTOGRe.FindStringSubmatch(t); m != nil {
		o.Info["tog"] = groupThousands(atoi(m[1])) + " lb"
		l := "Take-off weight " + o.Info["tog"]
		if c := unitedWtChgRe.FindStringSubmatch(t); c != nil {
			l += fmt.Sprintf(" (%s%s lb from the last)", c[1], groupThousands(atoi(c[2])))
		}
		lines = append(lines, l)
	}
	if m := unitedWtZFWRe.FindStringSubmatch(t); m != nil {
		o.Info["zfw"] = groupThousands(atoi(m[1])) + " lb"
		lines = append(lines, "Zero fuel weight "+o.Info["zfw"])
	}
	if m := unitedWtCGRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, "Centre of gravity "+m[1]+"% MAC")
	}
	if m := unitedWtSOBRe.FindStringSubmatch(t); m != nil {
		o.Info["souls"] = m[1]
		lines = append(lines, m[1]+" people on board")
	}
	if m := unitedWtPaxRe.FindStringSubmatch(t); m != nil {
		o.Info["pax"] = m[3]
		lines = append(lines, fmt.Sprintf("%s passengers (%s and %s by cabin)", m[3], m[1], m[2]))
	}
	if m := unitedWtCrwRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Crew %s flight deck and %s cabin", m[1], m[2]))
	}
	return lines
}

var (
	unitedGateRe  = regexp.MustCompile(`GATE +(\S+) +AVAIL +(\d{4})`)
	unitedRampRe  = regexp.MustCompile(`RAMP FREQ +([\d.]+)`)
	unitedOpsRe   = regexp.MustCompile(`OPS FREQ +([\d.]+)`)
	unitedPwrRe   = regexp.MustCompile(`GROUND PWR AVAIL -(\w+)-`)
	unitedGroundR = regexp.MustCompile(`PLANNED GROUND TIME\s+IS (\d\d:\d\d)`)
	unitedNextRe  = regexp.MustCompile(`NEXT FLT UA0*(\d+)/(\d\d)\s+([A-Z]{4}) ([A-Z]{4}) ETD (\d{4})Z`)
)

func unitedGate(t string, o *OpsReport) (lines []string) {
	if m := unitedGateRe.FindStringSubmatch(t); m != nil {
		o.Info["gate"] = m[1]
		lines = append(lines, fmt.Sprintf("Gate %s, available at %sZ", m[1], m[2]))
	}
	if m := unitedRampRe.FindStringSubmatch(t); m != nil {
		o.Info["ramp_freq"] = m[1]
		lines = append(lines, "Ramp frequency "+m[1])
	}
	if m := unitedOpsRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, "Operations frequency "+m[1])
	}
	if m := unitedPwrRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, "Ground power available: "+strings.ToLower(m[1]))
	}
	if m := unitedGroundR.FindStringSubmatch(t); m != nil {
		lines = append(lines, "Planned ground time "+m[1])
	}
	if m := unitedNextRe.FindStringSubmatch(t); m != nil {
		o.Info["next"] = "UA" + m[1]
		lines = append(lines, fmt.Sprintf("Next flight UA%s %s→%s, departing %sZ", m[1], m[3], m[4], m[5]))
	}
	return lines
}

var (
	unitedTbAtRe  = regexp.MustCompile(`AT (\d{4})Z (FL\d+)`)
	unitedTbSevRe = regexp.MustCompile(`SEVERITY (\w+)`)
	unitedTbTypRe = regexp.MustCompile(`TYPE (\w+)`)
	unitedTbAltRe = regexp.MustCompile(`ALTITUDE (FL\d+) - (FL\d+)`)
	unitedTbValRe = regexp.MustCompile(`VALID (\d+ [A-Z]{3} \d{4}Z)\s+TO (\d+ [A-Z]{3} \d{4}Z)`)
)

func unitedTurb(t string, o *OpsReport) (lines []string) {
	sev := map[string]string{"LGT": "light", "MOD": "moderate", "SEV": "severe", "EXTRM": "extreme"}
	if m := unitedTbSevRe.FindStringSubmatch(t); m != nil {
		s := sev[m[1]]
		if s == "" {
			s = strings.ToLower(m[1])
		}
		o.Info["severity"] = s
		lines = append(lines, "Severity: "+s)
	}
	if m := unitedTbTypRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, "Type: "+m[1])
	}
	if m := unitedTbAltRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Altitudes %s to %s", m[1], m[2]))
	}
	if m := unitedTbAtRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("The flight enters the area at %sZ at %s", m[1], m[2]))
	}
	if m := unitedTbValRe.FindStringSubmatch(t); m != nil {
		lines = append(lines, fmt.Sprintf("Valid %s to %s", m[1], m[2]))
	}
	return lines
}
