package decoder

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind classifies what a message body is.
type Kind string

const (
	KindADSC     Kind = "adsc"
	KindCPDLC    Kind = "cpdlc"
	KindAFN      Kind = "afn"
	KindMIAM     Kind = "miam"
	KindMedia    Kind = "media_advisory"
	KindDATIS    Kind = "datis_request"
	KindFMS      Kind = "fms_position"
	KindPlan     Kind = "flight_plan"
	KindProgress Kind = "progress"
	KindACMF     Kind = "acmf_report"
	KindAOC      Kind = "aoc_position"
	KindPOS      Kind = "pos_report"
	KindPOSRPT   Kind = "posrpt"
	KindPOSA1    Kind = "posa1"
	KindSBPOS    Kind = "sbpos"
	KindUnknown  Kind = "unknown"
)

// Message is the raw input. Label and Dir may be empty/Unknown, in which case
// they are inferred from the text.
type Message struct {
	Label string
	Dir   Direction
	Text  string
}

type Identity struct {
	Tail     string // from the ARINC 622 address or an AFN FMH segment
	Callsign string // from an AFN FMH segment
	FlightID string // 6-char prefix field as sent, e.g. "CX0833"
	Flight   string // normalised, e.g. "CX833"
	MsgNum   string // 4-char prefix field, e.g. "J27A"
}

type Result struct {
	Kind     Kind
	Label    string
	Dir      Direction
	Prefix   string // the stripped 10-char (or shorter) prefix, if any
	Body     string // the text handed to the decoders
	Identity Identity
	Tree     map[string]any // libacars output, nil if libacars decoded nothing
	Fields   map[string]any // our own hand-parsed fields

	Summary string     // one line for a timeline
	Text    string     // multi-line human-readable decode (libacars text, else Summary)
	Pos     *Pos       // a reported position, if the message carries one
	Route   []Waypoint // ADS-C predicted route, if any

	Wx    *Weather // wind and temperature at the report, if it carries them
	GS    *float64 // ground speed in knots, if reported
	Track *float64 // true track in degrees, if reported
	Fixes []FixRef // waypoints named by a CPDLC position report
}

var (
	// bodyAnchor matches text that is already a bare application body.
	bodyAnchor = regexp.MustCompile(`^(/|T[-0-9]|0[EL][A-Z0-9])`)
	flightID   = regexp.MustCompile(`^([A-Z0-9]{2})(\d{1,4})[A-Z]?$`)
	ownerAddr  = regexp.MustCompile(`^/[A-Z0-9]{7}\.([A-Z0-9]{3})\.`)
	// Some VDL2 traffic arrives with the leading "/" missing.
	noSlashARINC = regexp.MustCompile(`^[A-Z0-9]{7}\.(ADS|AT1|CR1|CC1|DR1|AFN|TI2)[./]`)
)

// SplitPrefix separates the 4-char message number + 6-char flight ID that
// airframes.io text often carries. Text that is already a bare body is
// returned unchanged. As a fallback, everything before a "/" in the first 10
// characters is treated as prefix.
func SplitPrefix(text string) (prefix, body string) {
	if bodyAnchor.MatchString(text) {
		return "", text
	}
	if len(text) > 10 && flightID.MatchString(text[4:10]) && bodyAnchor.MatchString(text[10:]) {
		return text[:10], text[10:]
	}
	if i := strings.IndexByte(text, '/'); i > 0 && i <= 10 {
		return text[:i], text[i:]
	}
	return "", text
}

// parsePrefix extracts message number and flight from a 10-char prefix.
func parsePrefix(prefix string) (id Identity) {
	if len(prefix) != 10 {
		return
	}
	id.MsgNum = prefix[:4]
	id.FlightID = prefix[4:]
	if m := flightID.FindStringSubmatch(id.FlightID); m != nil {
		n, _ := strconv.Atoi(m[2])
		id.Flight = m[1] + strconv.Itoa(n)
	}
	return
}

// classify decides the kind of body and a default label/direction when the
// caller did not supply them.
func classify(label, body string) (Kind, string, Direction) {
	var k Kind
	defLabel, defDir := "", Downlink
	if m := ownerAddr.FindStringSubmatch(body); m != nil {
		switch m[1] {
		case "ADS":
			k, defLabel = KindADSC, "B6"
		case "AT1", "CR1", "CC1", "DR1":
			k, defLabel = KindCPDLC, "BA"
		}
	}
	switch {
	case k != "":
	case posa1Re.MatchString(body):
		k, defLabel = KindPOSA1, "16"
	case sbPosRe.MatchString(body):
		k, defLabel = KindSBPOS, "H1"
	case posnRe.MatchString(body):
		k, defLabel = KindFMS, "H1"
	case acmfHeader.MatchString(body) || acmfPoint.MatchString(body):
		k, defLabel = KindACMF, "H1"
	case posRptRe.MatchString(body):
		k, defLabel = KindPOS, "H1"
	case posrptRe.MatchString(body):
		k, defLabel = KindPOSRPT, "80"
	case aocRe.MatchString(body):
		k, defLabel = KindAOC, "83"
	case strings.Contains(body, ".AFN/"):
		k, defLabel = KindAFN, "B0"
	case strings.Contains(body, ".TI2/"):
		k, defLabel = KindDATIS, "B9"
	case label == "SA" || strings.HasPrefix(body, "0E") || strings.HasPrefix(body, "0L"):
		k, defLabel = KindMedia, "SA"
	case label == "MA" || (len(body) > 1 && body[0] == 'T' && (body[1] == '-' || body[1] >= '0' && body[1] <= '9')):
		k, defLabel = KindMIAM, "MA"
	default:
		k = KindUnknown
	}
	if label == "" {
		label = defLabel
	}
	dir := defDir
	if len(label) > 0 && label[0] == 'A' {
		dir = Uplink
	}
	return k, label, dir
}

// Decode strips the prefix, works out label and direction, runs libacars where
// it applies, and runs our own parsers for the rest. Identity is always derived
// from the raw text, never from airframes.io metadata.
func Decode(m Message) (Result, error) {
	var wrapped Identity // the message number and flight in a satellite header, if there was one
	if inner, label, prefix, ok := unwrapSatcom(m.Text); ok {
		m.Text = inner
		if label != "" {
			m.Label = label
		}
		wrapped = parsePrefix(prefix)
	}
	if r, ok := decodePlan(m); ok {
		return r, nil
	}
	prefix, body := SplitPrefix(m.Text)
	if strings.HasPrefix(m.Text, "POSA1") || strings.HasPrefix(m.Text, "SB0122BA_") {
		prefix, body = "", m.Text // the latitude digits after POSA1N look like a message number
	}
	body = strings.TrimRight(body, "\r\n")
	if noSlashARINC.MatchString(body) {
		body = "/" + body
	}
	r := Result{Prefix: prefix, Body: body, Identity: parsePrefix(prefix), Fields: map[string]any{}}
	if r.Identity.FlightID == "" && wrapped.FlightID != "" {
		r.Identity.MsgNum, r.Identity.FlightID, r.Identity.Flight = wrapped.MsgNum, wrapped.FlightID, wrapped.Flight
	}

	var dir Direction
	r.Kind, r.Label, dir = classify(m.Label, body)
	if m.Label != "" && m.Dir == Unknown && m.Label[0] != 'A' && m.Label[0] != 'B' {
		dir = Downlink // MA, SA, ... are observed as downlinks
	}
	r.Dir = m.Dir
	if r.Dir == Unknown {
		r.Dir = dir
	}

	switch r.Kind {
	case KindADSC, KindCPDLC, KindMIAM:
		d, err := DecodeAppsFull(r.Label, body, r.Dir)
		if err != nil {
			return r, err
		}
		if m.Dir == Unknown && (r.Kind == KindADSC || r.Kind == KindCPDLC) && parseFailed(d) {
			// The direction was a guess and the text does not parse that way: try the other.
			other := map[Direction]Direction{Uplink: Downlink, Downlink: Uplink}[r.Dir]
			if d2, err2 := DecodeAppsFull(r.Label, body, other); err2 == nil && d2 != nil && !parseFailed(d2) {
				d, r.Dir = d2, other
			}
		}
		if d != nil {
			r.Tree, r.Text = d.Tree, d.Text
		}
		if arinc, ok := r.Tree["arinc622"].(map[string]any); ok {
			if a, _ := arinc["air_addr"].(string); a != "" {
				r.Identity.Tail = strings.TrimLeft(a, ".")
			}
			if g, _ := arinc["gs_addr"].(string); g != "" {
				r.Fields["gs_addr"] = g
			}
		}
		if r.Kind == KindMIAM {
			if r.Identity.Tail == "" {
				if a, ok := findString(r.Tree, "aircraft_id"); ok {
					r.Identity.Tail = strings.TrimLeft(a, ".")
				}
			}
			if rep, ok := ParseREP081(body); ok {
				r.Fields["rep081"] = rep
				alt := float64(rep.AltFt)
				r.Pos = &Pos{Lat: rep.Lat, Lon: rep.Lon, AltFt: &alt, Secs: float64(rep.SecsInMonth), Basis: BasisMonth}
				dir, kt, temp := float64(rep.WindDirDeg), float64(rep.WindKt), rep.TempC
				r.Wx = &Weather{WindDirDeg: &dir, WindKt: &kt, TempC: &temp}
			}
		}
		if r.Kind == KindCPDLC && r.Tree != nil {
			var fix string
			if r.Pos, fix = cpdlcPosition(r.Tree); fix != "" {
				r.Fields["fix"] = fix
			}
			r.GS, r.Track, r.Wx, r.Fixes = cpdlcExtras(r.Tree)
		}
		if r.Kind == KindADSC {
			r.Pos, r.Route = adscPosition(r.Tree)
			r.GS, r.Track, r.Wx = adscMotionWeather(r.Tree)
		}
	case KindFMS:
		parseFMS(body, &r)
	case KindACMF:
		parseACMF(body, &r)
	case KindAOC:
		parseAOC(body, &r)
	case KindPOS:
		parsePosRpt(body, &r)
	case KindPOSRPT:
		parsePOSRPT(body, &r)
	case KindPOSA1:
		parsePOSA1(body, &r)
	case KindSBPOS:
		parseSBPos(body, &r)
	case KindAFN:
		parseAFN(body, &r)
	case KindDATIS:
		parseDATIS(body, &r)
	case KindMedia:
		parseMedia(body, &r)
	}
	r.Summary = summarize(r)
	if r.Text == "" {
		r.Text = r.Summary
	}
	return r, nil
}

// findString returns the first string value stored under key anywhere in v.
func findString(v any, key string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t[key].(string); ok {
			return s, true
		}
		for _, c := range t {
			if s, ok := findString(c, key); ok {
				return s, true
			}
		}
	case []any:
		for _, c := range t {
			if s, ok := findString(c, key); ok {
				return s, true
			}
		}
	}
	return "", false
}

// parseFailed reports whether libacars could not make sense of an ARINC 622
// ADS-C or CPDLC message in the direction it was given.
func parseFailed(d *Decoded) bool {
	if d == nil {
		return true
	}
	a, _ := d.Tree["arinc622"].(map[string]any)
	for _, k := range []string{"adsc", "cpdlc"} {
		if app, _ := a[k].(map[string]any); app != nil && app["err"] == true {
			return true
		}
	}
	return false
}
