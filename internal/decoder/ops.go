package decoder

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avanha/fullflight/internal/airlines"
)

// OpsReport is an airline's own operations message: a movement report (Qantas "MVA", Air Canada's out/off/on/in
// messages), a flight's route and number (Air Canada's flight information, Qantas's flight data link), a load sheet,
// or a clearance. They matter for the route they name and the times they give. Times are hhmm UTC as sent, on the day
// the message was sent or the day before.
type OpsReport struct {
	Airline  string            `json:"airline,omitempty"`
	Event    string            `json:"event"`              // the message type as the airline writes it: AD, AA, KQ1, LDF, PDC ...
	Flight   string            `json:"flight,omitempty"`   // IATA style, QF8
	Callsign string            `json:"callsign,omitempty"` // ICAO style, QFA8
	Reg      string            `json:"reg,omitempty"`
	Origin   string            `json:"origin,omitempty"` // ICAO
	Dest     string            `json:"dest,omitempty"`
	Out      string            `json:"out,omitempty"` // leaving the gate
	Off      string            `json:"off,omitempty"` // wheels up
	On       string            `json:"on,omitempty"`  // wheels down
	In       string            `json:"in,omitempty"`  // at the gate
	ETA      string            `json:"eta,omitempty"`
	At       string            `json:"at,omitempty"`     // when the event happened, RFC 3339 UTC, for reports that give a full date
	OffAt    string            `json:"off_at,omitempty"` // wheels up as an exact time, for a report that gives one
	Sched    string            `json:"sched,omitempty"`  // when a flight that has not left yet is due to, RFC 3339 UTC
	Runway   string            `json:"runway,omitempty"`
	SID      string            `json:"sid,omitempty"`
	Squawk   string            `json:"squawk,omitempty"`
	Phase    string            `json:"phase,omitempty"`
	Info     map[string]string `json:"info,omitempty"` // whatever else the message gives, as sent
}

type opsParsed struct {
	rep     OpsReport
	pos     *Pos
	wx      *Weather
	summary string
	extra   []string // more lines for the message text
	uplink  bool
	tail    string // the registration the message names
}

var spaces = regexp.MustCompile(`\s+`)

// opsFlight gives the callsign and IATA flight number for an airline code and a number written with leading zeros.
func opsFlight(code, digits string) (callsign, flight string) {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", ""
	}
	icao, iata := airlines.Forms(code + strconv.Itoa(n))
	return icao, iata
}

// opsAirline names the airline of an ICAO callsign prefix, for those that send operations messages we read.
func opsAirline(code string) string {
	return map[string]string{"ACA": "Air Canada", "UAL": "United", "CKK": "China Cargo", "TZP": "Zipair", "QFA": "Qantas", "AVA": "Avianca", "ASA": "Alaska", "EVA": "EVA Air", "DAL": "Delta"}[code]
}

// hhmm accepts "1921" or "1921Z" and rejects the asterisks a message puts where a time is not yet known.
func hhmm(s string) string {
	s = strings.TrimSuffix(s, "Z")
	if len(s) != 4 {
		return ""
	}
	h, e1 := strconv.Atoi(s[:2])
	m, e2 := strconv.Atoi(s[2:])
	if e1 != nil || e2 != nil || h > 23 || m > 59 {
		return ""
	}
	return s
}

func hmZ(s string) string {
	if len(s) != 4 {
		return s
	}
	return s[:2] + ":" + s[2:] + "Z"
}

func route(o, d string) string {
	switch {
	case o != "" && d != "":
		return o + "→" + d
	case d != "":
		return "at " + d
	case o != "":
		return "at " + o
	}
	return ""
}

func (o OpsReport) name() string {
	switch {
	case o.Flight != "":
		return o.Flight
	case o.Callsign != "":
		return o.Callsign
	}
	return ""
}

// times lists the movement times a report gave, in the order the flight goes through them.
func (o OpsReport) times() string {
	var s []string
	for _, t := range []struct{ name, v string }{{"out", o.Out}, {"off", o.Off}, {"on", o.On}, {"in", o.In}} {
		if t.v != "" {
			s = append(s, t.name+" "+hmZ(t.v))
		}
	}
	if o.ETA != "" {
		s = append(s, "ETA "+hmZ(o.ETA))
	}
	return strings.Join(s, ", ")
}

// parseOps reads the airline messages we know, by the label they came on.
func parseOps(label, text string) (opsParsed, bool) {
	switch label {
	case "80", "81":
		if p, ok := parseQantasMVA(text); ok {
			return p, true
		}
		return parseAlaskaMovement(text)
	case "RF":
		return parseQantasFDA(text)
	case "5U":
		if p, ok := parseQantasWX(text); ok {
			return p, true
		}
		return parseWXRQ(text)
	case "11", "12", "13", "14":
		return parseEVAMovement(text)
	case "4T", "4Q":
		return parseACMovement(text)
	case "49", "17":
		return parseEvent(text)
	case "4C":
		return parseACFlight(text)
	case "4M":
		return parseACLoad(text)
	case "4P":
		return parseACNotoc(text)
	case "A3", "A4":
		return parseClearance(text)
	case "H1":
		if p, ok := parseEngine(text); ok {
			return p, true
		}
		if p, ok := parseEB(text); ok {
			return p, true
		}
		for _, parse := range []func(string) (opsParsed, bool){parseABS, parseAlaskaTakeoff, parseAMDAR} {
			if p, ok := parse(text); ok {
				return p, true
			}
		}
		return opsParsed{}, false
	case "RA", "25":
		return parseDelta(text)
	case "83":
		return parsePOSRPT83(text)
	case "99":
		return parseEMR(text)
	case "24":
		return parseTKO(text)
	}
	return opsParsed{}, false
}

// Qantas movement messages, labels 80 (departure) and 81 (arrival):
//
//	MVA QFA0007/25.VHOQG.YSSY AD0213/0233 EA1725 KDFW     out 02:13, off 02:33, ETA 17:25 at KDFW
//	MVA QFA0008/24.VHOQG.YSSY AA2008/2012                  on 20:08, in 20:12 at YSSY
//
// The first message of each pair has the first time only. The day is the flight's date. Read from samples.
var mvaRe = regexp.MustCompile(`MVA ([A-Z]{3})(\d{4})/ ?(\d{2})? ?\.([A-Z0-9-]+)\. ?([A-Z]{4})? ?(A[DA])(\d{4})(?:/(\d{4}))?(?: EA(\d{4}))?(?: ([A-Z]{4}))?`)

func parseQantasMVA(text string) (opsParsed, bool) {
	m := mvaRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Qantas", Event: m[6], Reg: m[4], ETA: hhmm(m[9])}
	o.Callsign, o.Flight = opsFlight(m[1], m[2])
	if m[3] != "" {
		o.Info = map[string]string{"day": m[3]}
	}
	t1, t2 := hhmm(m[7]), hhmm(m[8])
	verb := ""
	switch m[6] {
	case "AD":
		o.Origin, o.Dest = icaoOf(m[5]), icaoOf(m[10])
		o.Out, o.Off = t1, t2
		verb = "departure"
	default:
		o.Dest = icaoOf(m[5])
		o.On, o.In = t1, t2
		verb = "arrival"
	}
	s := strings.TrimSpace(fmt.Sprintf("Qantas %s %s %s", o.name(), verb, route(o.Origin, o.Dest)))
	if t := o.times(); t != "" {
		s += ": " + t
	}
	return opsParsed{rep: o, summary: s}, true
}

// Qantas flight data link, label RF. A flight's data is submitted, committed and requested with its route, number and
// scheduled time; the other messages in the exchange carry numbers only:
//
//	,FDASUB01261025012233,SYD,,DFW,,QF007,261025,0205,388,,VH-OQG,/343940,350,,1,1545
//	,FDAACK01261025004030/344244,350,,1,B8F2
//
// What the numbers mean is not known; they are kept as sent.
var (
	fdaRouteRe = regexp.MustCompile(`^(?:[A-Z0-9]{10})?,(FDASUB|FDACOM|FSTREQ)\d{2}(\d{12}),([A-Z]{3}),,([A-Z]{3}),,([A-Z0-9]{4,6}),(\d{6}),(\d{4}),(\w+),,([A-Z0-9-]+),/?(.*?),?[0-9A-F]{4}$`)
	fdaOtherRe = regexp.MustCompile(`^(?:[A-Z0-9]{10})?,(FDAACK|FSTREP|FENIND|FERIND|FDASUB|FDACOM|FSTREQ)\d{2}(\d{12})/?(.*?),?[0-9A-F]{4}$`)
)

var fdaWhat = map[string]string{
	"FDASUB": "flight data submitted", "FDACOM": "flight data committed", "FSTREQ": "status requested",
	"FDAACK": "flight data acknowledged", "FSTREP": "status reply", "FENIND": "end indication", "FERIND": "error indication",
}

func parseQantasFDA(text string) (opsParsed, bool) {
	t := strings.TrimSpace(text)
	if m := fdaRouteRe.FindStringSubmatch(t); m != nil {
		o := OpsReport{Airline: "Qantas", Event: m[1], Origin: icaoOf(m[3]), Dest: icaoOf(m[4]), Reg: m[9],
			Info: map[string]string{"scheduled": m[7] + "Z", "date": m[6], "type": m[8]}}
		if d, err := time.Parse("060102 1504", m[6]+" "+m[7]); err == nil {
			o.Sched = d.UTC().Format(time.RFC3339)
		}
		if len(m[5]) > 2 {
			o.Callsign, o.Flight = opsFlight(m[5][:2]+"A", m[5][2:])
			o.Flight = m[5][:2] + strconv.Itoa(atoi(m[5][2:]))
		}
		if m[10] != "" {
			o.Info["data"] = m[10]
		}
		s := fmt.Sprintf("Qantas %s: %s %s, scheduled %s", fdaWhat[m[1]], o.Flight, route(o.Origin, o.Dest), hmZ(m[7]))
		return opsParsed{rep: o, summary: s, uplink: m[1] != "FDAACK"}, true
	}
	if m := fdaOtherRe.FindStringSubmatch(t); m != nil {
		o := OpsReport{Airline: "Qantas", Event: m[1], Info: map[string]string{"data": m[3]}}
		return opsParsed{rep: o, summary: "Qantas " + fdaWhat[m[1]]}, true
	}
	return opsParsed{}, false
}

// Qantas weather requests from the cockpit, label 5U:
//
//	WX REQUEST / QFA0064/31 / ATIS   / YSSY YMML       D-ATIS for two airports
//	WX REQUEST / QFA0064/31 / TAFOR  / YSSY            a forecast
var qfWxRe = regexp.MustCompile(`WX REQUEST ([A-Z]{3})(\d{4})/(\d{2})? ?(\w+) ([A-Z]{4}(?: [A-Z]{4})*)$`)

var wxWord = map[string]string{"TAFOR": "TAF", "ACTUAL": "METAR"}

// Fiji Airways and Delta's weather request, label 5U: "01 WXRQ   0811/24 KLAX/NFFN .DQ-FAM / /TYP 1/STA NFFN/STA NSFA".
var wxrqRe = regexp.MustCompile(`WXRQ +(\d{4})/(\d{2}) ([A-Z]{4})/([A-Z]{4}) +\.([A-Z0-9-]+)((?: ?/?TYP \d+)?(?: ?/STA [A-Z]{4})*)`)

func parseWXRQ(text string) (opsParsed, bool) {
	m := wxrqRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	var stations []string
	for _, s := range regexp.MustCompile(`STA ([A-Z]{4})`).FindAllStringSubmatch(m[6], -1) {
		stations = append(stations, s[1])
	}
	o := OpsReport{Event: "WXRQ", Origin: m[3], Dest: m[4], Reg: m[5], Info: map[string]string{"day": m[2]}}
	o.Callsign = "" // the flight number is not a callsign (0811 is Fiji 811)
	s := fmt.Sprintf("Weather request %s", route(o.Origin, o.Dest))
	if len(stations) > 0 {
		s += " for " + strings.Join(stations, ", ")
	}
	return opsParsed{rep: o, summary: s}, true
}

func parseQantasWX(text string) (opsParsed, bool) {
	m := qfWxRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Qantas", Event: "WX " + m[4], Info: map[string]string{"day": m[3], "airports": m[5]}}
	o.Callsign, o.Flight = opsFlight(m[1], m[2])
	list := strings.Split(m[5], " ")
	kind := m[4]
	if w, ok := wxWord[kind]; ok {
		kind = w
	}
	s := fmt.Sprintf("Weather request (%s) for %s", kind, strings.Join(list, ", "))
	if m[4] == "ATIS" {
		s = "D-ATIS request for " + strings.Join(list, ", ")
	}
	return opsParsed{rep: o, summary: s}, true
}

// Air Canada's out/off/on/in messages (labels 4T and 4Q). The flight is AC0025/31/31 (number, then the date twice);
// the airport is the one the aircraft is at, three letters:
//
//	ATD AC0025/31/31 YVR 1921Z                              left the gate
//	PSHBK AC0025/31/31 YVR 1921Z/****Z/****Z/1922Z         pushing back
//	TDX AC0025/31/31 YVR 1921Z 1940Z 833  0791/089528 L A   out and off
//	ARV AC0020/31/31 YVR 1427Z                              landed
//	TAR AC0020/31/31 YVR 1427Z 1434Z 8330068/0795           on and in
//	KQ1 AC0020/31/31 SIN YVR/0037/0057/1427/1434            the whole flight: out, off, on, in
//	AGFSR AC0025/31/31/YVRPVG/2218Z/833/5847.5N15356.8W/340/CRUISE/0593/0199/M41/214017/...   a position report
//
// Read from samples, not a specification.
var (
	acHeadRe = regexp.MustCompile(`^(ATD|ARV|PSHBK|TDX|TAR|KQ1) (AC)(\d{4})/(\d{2})/\d{2} ([A-Z]{3})(?: ([A-Z]{3}))?[ /]?(.*)$`)
	acPosRe  = regexp.MustCompile(`^AGFSR (AC)(\d{4})/(\d{2})/\d{2}/([A-Z]{3})([A-Z]{3})/(\d{4})Z/(\w+)/(\d{2})(\d{2}\.\d)([NS])(\d{3})(\d{2}\.\d)([EW])/(\d{3})/([A-Z ]+)/(\d+)/(\d+)/([MP])(\d+)/(\d{3})(\d{2,3})`)
	acTimeRe = regexp.MustCompile(`\d{4}Z|\*{4}Z`)
)

func parseACMovement(text string) (opsParsed, bool) {
	t := spaces.ReplaceAllString(strings.TrimSpace(text), " ")
	if m := acPosRe.FindStringSubmatch(t); m != nil {
		return acPosition(m), true
	}
	m := acHeadRe.FindStringSubmatch(t)
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Air Canada", Event: m[1], Info: map[string]string{"day": m[4]}}
	o.Callsign, o.Flight = opsFlight(m[2], m[3])
	here := icaoOf(m[5])
	times := func(s string) []string {
		var out []string
		for _, x := range acTimeRe.FindAllString(s, -1) {
			out = append(out, hhmm(x))
		}
		return out
	}
	get := func(ts []string, i int) string {
		if i < len(ts) {
			return ts[i]
		}
		return ""
	}
	switch m[1] {
	case "ATD", "PSHBK", "TDX":
		o.Origin = here
		ts := times(t)
		o.Out = get(ts, 0)
		if m[1] == "TDX" {
			o.Off = get(ts, 1)
		}
	case "ARV", "TAR":
		o.Dest = here
		ts := times(t)
		o.On = get(ts, 0)
		if m[1] == "TAR" {
			o.In = get(ts, 1)
		}
	case "KQ1":
		o.Origin, o.Dest = here, icaoOf(m[6])
		f := strings.Split(m[7], "/")
		if len(f) >= 4 {
			o.Out, o.Off, o.On, o.In = hhmm(f[0]), hhmm(f[1]), hhmm(f[2]), hhmm(f[3])
		}
	}
	s := fmt.Sprintf("Air Canada %s %s", o.name(), map[string]string{
		"ATD": "left the gate", "PSHBK": "pushing back", "TDX": "left the gate and took off", "ARV": "landed",
		"TAR": "landed and reached the gate", "KQ1": "movements"}[m[1]])
	if r := route(o.Origin, o.Dest); r != "" {
		s += " " + r
	}
	if tm := o.times(); tm != "" {
		s += ": " + tm
	}
	return opsParsed{rep: o, summary: s}, true
}

func acPosition(m []string) opsParsed {
	o := OpsReport{Airline: "Air Canada", Event: "AGFSR", Origin: icaoOf(m[4]), Dest: icaoOf(m[5]), Phase: strings.ToLower(m[15]),
		Info: map[string]string{"day": m[3], "time": m[6] + "Z"}}
	o.Callsign, o.Flight = opsFlight(m[1], m[2])
	atof := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	lat := atof(m[8]) + atof(m[9])/60
	lon := atof(m[11]) + atof(m[12])/60
	if m[10] == "S" {
		lat = -lat
	}
	if m[13] == "W" {
		lon = -lon
	}
	hh, mm := atoi(m[6][:2]), atoi(m[6][2:])
	if hh > 23 || mm > 59 || lat > 90 || lon > 180 {
		return opsParsed{}
	}
	alt := atof(m[14]) * 100
	p := &Pos{Lat: lat, Lon: lon, AltFt: &alt, Secs: float64(hh*3600 + mm*60), Basis: BasisDay}
	dir, kt := atof(m[20]), atof(m[21])
	temp := atof(m[19])
	if m[18] == "M" {
		temp = -temp
	}
	wx := &Weather{TempC: &temp}
	if dir <= 360 {
		wx.WindDirDeg, wx.WindKt = &dir, &kt
	}
	s := fmt.Sprintf("Air Canada %s position report %s: %.3f%s %.3f%s, FL%d, %s", o.name(), route(o.Origin, o.Dest),
		abs(lat), map[bool]string{true: "N", false: "S"}[lat >= 0], abs(lon), map[bool]string{true: "E", false: "W"}[lon >= 0], int(alt/100), o.Phase)
	return opsParsed{rep: o, pos: p, wx: wx, summary: s}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Ground events from an aircraft's computer (labels 49 and 17: Air Canada, United, China Cargo, Zipair), a four letter
// event, the flight, the day and time and the route in ICAO codes:
//
//	01ICCL    ACA025/311941CYVRZSPD / + 1888221.4+ 13.0
//	01DCAPUAL182    /220652KDENEDDF / + 1597158.6+  9.7
//
// The flight, or the airports, can be asterisks. What the event codes mean is not known (TOIC is at the time the
// aircraft took off in Air Canada's movement messages, ICCL a minute after); they are shown as sent, and the rest of
// the line is kept.
var eventRe = regexp.MustCompile(`^01([A-Z]{4}) *([A-Z]{3}\d{1,4})? */(\d{2})(\d{4})([A-Z]{4}|\*+)([A-Z]{4}|\*+) ?/? ?(.*)$`)

func parseEvent(text string) (opsParsed, bool) {
	m := eventRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Event: m[1], Info: map[string]string{"day": m[3], "time": hmZ(m[4]), "data": m[7]}}
	if len(m[5]) == 4 && m[5][0] != '*' {
		o.Origin = m[5]
	}
	if len(m[6]) == 4 && m[6][0] != '*' {
		o.Dest = m[6]
	}
	if f := m[2]; f != "" {
		o.Callsign, o.Flight = opsFlight(f[:3], f[3:])
		if o.Callsign == "" {
			o.Callsign = f
		}
		o.Airline = opsAirline(f[:3])
	}
	who := o.name()
	s := strings.TrimSpace(fmt.Sprintf("%s %s event %s %s at %s", o.Airline, who, m[1], route(o.Origin, o.Dest), hmZ(m[4])))
	return opsParsed{rep: o, summary: strings.Join(strings.Fields(s), " ")}, true
}

// Air Canada's flight information, label 4C, sent to the aircraft before departure:
// "-FLT CA01 0025/31/YVR/PVG/CYVR/ZSPD/ACA025".
var acFlightRe = regexp.MustCompile(`^-?FLT \w+ (\d{4})/(\d{2})/([A-Z]{3})/([A-Z]{3})/([A-Z]{4})/([A-Z]{4})/(ACA\d+)`)

func parseACFlight(text string) (opsParsed, bool) {
	m := acFlightRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Air Canada", Event: "FLT", Origin: m[5], Dest: m[6], Info: map[string]string{"day": m[2]}}
	o.Callsign, o.Flight = opsFlight("AC", m[1])
	if c := m[7]; len(c) > 3 {
		o.Callsign = "ACA" + strconv.Itoa(atoi(c[3:]))
	}
	return opsParsed{rep: o, summary: fmt.Sprintf("Air Canada flight information %s %s", o.name(), route(o.Origin, o.Dest)), uplink: true}, true
}

// Air Canada's load sheet, label 4M ("LDF" final, "LDP" preliminary). Weights are in tonnes. The crew, passenger and
// infant counts follow the take-off weight.
var (
	acLoadHead = regexp.MustCompile(`LOAD (FINAL|PRELIM)\w* +AC(\d{4}) +(\d{2}[A-Z]{3}) +([A-Z]{3})/([A-Z]{3})`)
	acLoadVal  = func(name string) *regexp.Regexp {
		return regexp.MustCompile(name + `\*? +(\d+\.\d)`)
	}
	acLoadPax = regexp.MustCompile(`TOW +\d+\.\d +(\d+/\d+) +(\d+) +(\d+)`)
)

func parseACLoad(text string) (opsParsed, bool) {
	t := spaces.ReplaceAllString(text, " ")
	h := acLoadHead.FindStringSubmatch(t)
	if h == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Air Canada", Event: "load sheet", Origin: icaoOf(h[4]), Dest: icaoOf(h[5]), Info: map[string]string{"date": h[3], "status": strings.ToLower(h[1])}}
	o.Callsign, o.Flight = opsFlight("AC", h[2])
	var parts []string
	for _, v := range []struct{ key, name, label string }{{"zfw", "ZFW", "zero fuel"}, {"tow", "TOW", "take-off"}, {"fob", "FOB", "fuel"}} {
		if m := acLoadVal(v.name).FindStringSubmatch(t); m != nil {
			o.Info[v.key] = m[1]
			parts = append(parts, fmt.Sprintf("%s %s t", v.label, m[1]))
		}
	}
	if m := acLoadPax.FindStringSubmatch(t); m != nil {
		o.Info["crew"], o.Info["pax"], o.Info["infants"] = m[1], m[2], m[3]
		parts = append(parts, m[2]+" passengers")
	}
	s := fmt.Sprintf("Air Canada load sheet (%s) %s %s", o.Info["status"], o.name(), route(o.Origin, o.Dest))
	if len(parts) > 0 {
		s += ": " + strings.Join(parts, ", ")
	}
	return opsParsed{rep: o, summary: s, uplink: true}, true
}

// Air Canada's dangerous goods notice to the captain (label 4P, "PNT ... LOAD FINAL NOTOC AC0025 31OCT YVR/PVG").
var acNotocRe = regexp.MustCompile(`^-?/*\s*PNT \w+ .*?NOTOC AC(\d{4}) +(\d{2}[A-Z]{3}) +([A-Z]{3})/([A-Z]{3})`)

func parseACNotoc(text string) (opsParsed, bool) {
	m := acNotocRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Airline: "Air Canada", Event: "NOTOC", Origin: icaoOf(m[3]), Dest: icaoOf(m[4]), Info: map[string]string{"date": m[2]}}
	o.Callsign, o.Flight = opsFlight("AC", m[1])
	return opsParsed{rep: o, summary: fmt.Sprintf("Air Canada dangerous goods notice (NOTOC) %s %s", o.name(), route(o.Origin, o.Dest)), uplink: true}, true
}

// Pre-departure clearances over a ground station's data link (Beijing, Shanghai), labels A3 and A4, behind an
// address like /PEKDCYA.DC1/ (the clearance) or .FS1/ (the status of a request). A checksum of four hex digits ends
// the text:
//
//	CLD 1201 261028 ZBAA / PDC 267 / ACA030 CLRD TO CYVR OFF 36R VIA MUGL7X / SQUAWK 0075 ADT 1230 NEXT FREQ 121.600 ATIS F ...
//	FSM 1204 261028 ZBAA / ACA030 CDA RECEIVED / CLEARANCE CONFIRMED
var (
	clrHeadRe = regexp.MustCompile(`^/[A-Z0-9]{7}\.(DC1|FS1)/(CLD|FSM) (\d{4}) (\d{6}) ([A-Z]{4}) ?(.*?)(?:[0-9A-F]{4})?$`)
	clrBodyRe = regexp.MustCompile(`([A-Z0-9]+) CLRD TO ([A-Z]{4}) OFF (\w+) VIA (\w+)`)
	squawkRe  = regexp.MustCompile(`SQUAWK (\d{4})`)
	freqRe    = regexp.MustCompile(`NEXT FREQ ([\d.]+)`)
)

func parseClearance(text string) (opsParsed, bool) {
	m := clrHeadRe.FindStringSubmatch(spaces.ReplaceAllString(strings.TrimSpace(text), " "))
	if m == nil {
		return opsParsed{}, false
	}
	o := OpsReport{Event: m[2], Origin: m[5], Info: map[string]string{"sent": hmZ(m[3]), "date": m[4], "text": m[6]}}
	if m[2] == "CLD" {
		o.Event = "PDC"
		b := clrBodyRe.FindStringSubmatch(m[6])
		if b == nil {
			return opsParsed{}, false
		}
		o.Callsign, o.Dest, o.Runway, o.SID = b[1], b[2], b[3], b[4]
		if s := squawkRe.FindStringSubmatch(m[6]); s != nil {
			o.Squawk = s[1]
		}
		if f := freqRe.FindStringSubmatch(m[6]); f != nil {
			o.Info["next_freq"] = f[1]
		}
		s := fmt.Sprintf("Departure clearance %s %s: runway %s, %s", o.Callsign, route(o.Origin, o.Dest), o.Runway, o.SID)
		if o.Squawk != "" {
			s += ", squawk " + o.Squawk
		}
		o.Origin = m[5]
		return opsParsed{rep: o, summary: s, uplink: true}, true
	}
	// a status: the callsign (or registration) first, then what happened to its request
	who := strings.Fields(m[6])
	if len(who) > 0 && who[0] == "AN" && len(who) > 1 {
		who = who[1:]
	}
	if len(who) > 0 {
		o.Callsign = who[0]
	}
	what := "request status"
	switch {
	case strings.Contains(m[6], "REJECTED"):
		what = "request rejected"
		if i := strings.Index(m[6], "RESUBMIT YOUR REQUEST"); i >= 0 {
			if why := strings.TrimSpace(m[6][i+len("RESUBMIT YOUR REQUEST"):]); why != "" {
				what += " (" + strings.ToLower(why) + ")"
			}
		}
	case strings.Contains(m[6], "CONFIRMED"):
		what = "clearance confirmed"
	case strings.Contains(m[6], "RECEIVED"):
		what = "request received"
		if strings.Contains(m[6], "STANDBY") {
			what += ", standby"
		}
	}
	return opsParsed{rep: o, summary: fmt.Sprintf("%s clearance service: %s %s", m[5], o.Callsign, what), uplink: true}, true
}

func decodeOps(m Message) (Result, bool) {
	prefix, body := SplitPrefix(m.Text)
	p, ok := parseOps(m.Label, body)
	if !ok {
		prefix, body = "", m.Text
		if p, ok = parseOps(m.Label, body); !ok {
			return Result{}, false
		}
	}
	r := Result{Kind: KindOps, Label: m.Label, Dir: m.Dir, Prefix: prefix, Body: strings.TrimSpace(body), Fields: map[string]any{"ops": p.rep}, Pos: p.pos, Wx: p.wx, Identity: parsePrefix(prefix)}
	if r.Dir == Unknown {
		r.Dir = Downlink
		if p.uplink {
			r.Dir = Uplink
		}
	}
	if p.tail != "" {
		r.Identity.Tail = p.tail
	}
	switch {
	case p.rep.Event == "PDC":
		r.Identity = callsignIdentity(p.rep.Callsign)
	case p.rep.Flight != "":
		r.Identity.Callsign, r.Identity.Flight = p.rep.Callsign, p.rep.Flight
	}
	r.Summary = p.summary
	r.Text = p.summary
	if t := p.rep.times(); t != "" && !strings.Contains(p.summary, t) {
		r.Text += "\n" + t
	}
	for _, l := range p.extra {
		r.Text += "\n" + l
	}
	return r, true
}
