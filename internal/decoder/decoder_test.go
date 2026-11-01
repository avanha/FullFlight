package decoder

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Text     string    `json:"text"`
	Received time.Time `json:"received"`
}

func load(t *testing.T, name string) (fixture, Result) {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var all []fixture
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		if f.Name == name {
			r, err := Decode(Message{Label: f.Label, Text: f.Text})
			if err != nil {
				t.Fatal(err)
			}
			return f, r
		}
	}
	t.Fatalf("no fixture %q", name)
	return fixture{}, Result{}
}

// tag returns the contents of the first ADS-C tag with the given name.
func tag(t *testing.T, r Result, name string) map[string]any {
	t.Helper()
	tags := r.Tree["arinc622"].(map[string]any)["adsc"].(map[string]any)["tags"].([]any)
	for _, x := range tags {
		if v, ok := x.(map[string]any)[name]; ok {
			return v.(map[string]any)
		}
	}
	t.Fatalf("no %s tag in %v", name, tags)
	return nil
}

func near(t *testing.T, what string, got any, want, tol float64) {
	t.Helper()
	g, ok := got.(float64)
	if !ok || math.Abs(g-want) > tol {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestSplitPrefix(t *testing.T) {
	cases := []struct{ in, prefix, body string }{
		{"J25ACX0833/FUKJJYA.ADS..B-LRM07", "J25ACX0833", "/FUKJJYA.ADS..B-LRM07"},
		{"/OAKODYA.ADS..B-LRM03", "", "/OAKODYA.ADS..B-LRM03"},
		{"U99ACX0111T-2!<<abc", "U99ACX0111", "T-2!<<abc"},
		{"T02!<<abc", "", "T02!<<abc"},
		{"S96ACX01130LV162944S/", "S96ACX0113", "0LV162944S/"},
		{"0ES124056VS", "", "0ES124056VS"},
		{"XXXX/ABC", "XXXX", "/ABC"}, // fallback: strip before first "/"
	}
	for _, c := range cases {
		p, b := SplitPrefix(c.in)
		if p != c.prefix || b != c.body {
			t.Errorf("SplitPrefix(%q) = %q, %q; want %q, %q", c.in, p, b, c.prefix, c.body)
		}
	}
}

// Fixture 1.
func TestADSCPeriodic(t *testing.T) {
	for _, name := range []string{"adsc_periodic", "adsc_periodic_prefixed"} {
		_, r := load(t, name)
		if r.Kind != KindADSC || r.Label != "B6" || r.Dir != Downlink {
			t.Fatalf("%s: %v %s %v", name, r.Kind, r.Label, r.Dir)
		}
		if r.Identity.Tail != "B-LRM" {
			t.Errorf("%s: tail = %q", name, r.Identity.Tail)
		}
		b := tag(t, r, "basic_report")
		near(t, "lat", b["lat"], 27.5974846, 1e-5)
		near(t, "lon", b["lon"], 152.6006126, 1e-5)
		near(t, "alt", b["alt"], 38000, 0)
		near(t, "ts_sec", b["ts_sec"], 429, 0)
		near(t, "gs", tag(t, r, "earth_ref_data")["gnd_spd_kts"], 472.5, 0.01)
		near(t, "trk", tag(t, r, "earth_ref_data")["true_trk_deg"], 256.2, 0.01)
		near(t, "mach", tag(t, r, "air_ref_data")["spd_mach"], 0.8495, 1e-4)
		wp := tag(t, r, "predicted_route")["next_wpt"].(map[string]any)
		near(t, "next lat", wp["lat"], 27.0, 1e-3)
		near(t, "next lon", wp["lon"], 150.0, 1e-3)
		near(t, "next eta", wp["eta_sec"], 1088, 0)
	}
	if _, r := load(t, "adsc_periodic_prefixed"); r.Identity.Flight != "CX833" || r.Identity.FlightID != "CX0833" {
		t.Errorf("flight = %+v", r.Identity)
	}
}

// Fixture 2.
func TestADSCWaypointChange(t *testing.T) {
	_, r := load(t, "adsc_waypoint_change")
	b := tag(t, r, "wpt_change_event")
	near(t, "lat", b["lat"], 26.9968414, 1e-5)
	near(t, "lon", b["lon"], 149.9819183, 1e-5)
	near(t, "ts", b["ts_sec"], 1532, 0) // :25:32
	pr := tag(t, r, "predicted_route")
	near(t, "next lat", pr["next_wpt"].(map[string]any)["lat"], 25.0, 1e-3)
	near(t, "next eta", pr["next_wpt"].(map[string]any)["eta_sec"], 4130, 0)
	near(t, "then lat", pr["next_next_wpt"].(map[string]any)["lat"], 23.0043, 1e-3)
}

// Fixture 3, including the station-clock gotcha: embedded 19:09:30Z, logged 19:22Z.
func TestADSCTakeoffSEA(t *testing.T) {
	f, r := load(t, "adsc_takeoff_sea")
	near(t, "contract", tag(t, r, "ack")["contract_num"], 0, 0)
	b := tag(t, r, "basic_report")
	near(t, "lat", b["lat"], 47.4185371, 1e-5)
	near(t, "lon", b["lon"], -122.3075294, 1e-5)
	near(t, "alt", b["alt"], 1868, 0)
	near(t, "ts", b["ts_sec"], 570, 0)
	near(t, "gs", tag(t, r, "earth_ref_data")["gnd_spd_kts"], 162, 0.5)
	near(t, "vs", tag(t, r, "earth_ref_data")["vspd_ftmin"], 2704, 0)
	got := ResolveSecondsPastHour(b["ts_sec"].(float64), f.Received)
	if want := time.Date(2026, 10, 27, 19, 9, 30, 0, time.UTC); !got.Equal(want) {
		t.Errorf("resolved %v, want %v", got, want)
	}
}

// Fixture 4.
func TestADSCMeteo(t *testing.T) {
	_, r := load(t, "adsc_meteo")
	b := tag(t, r, "basic_report")
	near(t, "lat", b["lat"], -15.9914589, 1e-5)
	near(t, "lon", b["lon"], 142.0108223, 1e-5)
	near(t, "alt", b["alt"], 39004, 0)
	near(t, "ts", b["ts_sec"], 1691, 0) // :28:11
	m := tag(t, r, "meteo_data")
	near(t, "wind kt", m["wind_spd_kts"], 34.5, 0.01)
	near(t, "wind dir", m["wind_dir_true_deg"], 235.5, 0.1)
	near(t, "temp", m["temp_c"], -52.5, 0.01)
}

// Fixture 5.
func TestCPDLCPosition(t *testing.T) {
	_, r := load(t, "cpdlc_position")
	if r.Kind != KindCPDLC || r.Identity.Tail != "B-LRM" || r.Fields["gs_addr"] != "RPHIAYA" {
		t.Fatalf("%v %+v %v", r.Kind, r.Identity, r.Fields)
	}
	j, _ := json.Marshal(r.Tree)
	for _, want := range []string{`POSITION REPORT`, `"deg":19`, `SABNO`, `DALRA`, `UNTUL`} {
		if !strings.Contains(string(j), want) {
			t.Errorf("tree missing %s: %s", want, j)
		}
	}
}

// Fixture 6.
func TestCPDLCWilco(t *testing.T) {
	_, r := load(t, "cpdlc_wilco")
	j, _ := json.Marshal(r.Tree)
	for _, want := range []string{`"choice_label":"WILCO"`, `"msg_ref":6`, `"hour":4,"min":2,"sec":29`} {
		if !strings.Contains(string(j), want) {
			t.Errorf("tree missing %s: %s", want, j)
		}
	}
}

// A CPDLC message with more than one element is summarised by its first element, every time. (The summary used to
// take whichever label a walk over a Go map reached first, so it changed from one decode to the next.)
func TestCPDLCSummaryUsesFirstElement(t *testing.T) {
	m := Message{Label: "AA", Dir: Uplink, Text: "/USADCXA.AT1.B-221WA61B539D654E7C38330A7210A15B27"}
	for i := 0; i < 200; i++ {
		r, err := Decode(m)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && !strings.Contains(r.Text, "END SERVICE") {
			t.Fatalf("this message should carry a second element: %s", r.Text) // otherwise the test proves nothing
		}
		if want := "CPDLC uplink: CONTACT [icaounitname] [frequency]"; r.Summary != want {
			t.Fatalf("decode %d: summary %q, want %q", i, r.Summary, want)
		}
	}
}

func TestFindStringIsRepeatable(t *testing.T) {
	tree := map[string]any{"a": map[string]any{"x": "from a"}, "b": map[string]any{"x": "from b"}, "c": []any{map[string]any{"x": "from c"}}}
	for i := 0; i < 100; i++ {
		if s, _ := findString(tree, "x"); s != "from a" {
			t.Fatalf("got %q", s)
		}
	}
}

// Fixture 7, substituted by real samples: the original (a backfilled Oct 27 message) was not retrievable.
func TestMIAM(t *testing.T) {
	_, r := load(t, "rep081_handoff")
	if r.Kind != KindMIAM || r.Identity.Tail != "B-LRD" || r.Identity.Flight != "CX111" {
		t.Fatalf("%v %+v", r.Kind, r.Identity)
	}
	rep := r.Fields["rep081"].(REP081Report)
	near(t, "lat", rep.Lat, -16.919, 1e-3)
	near(t, "lon", rep.Lon, 142.488, 1e-3)
	if rep.Origin != "VHHH" || rep.Dest != "YSSY" || rep.AltFt != 39000 || rep.TempC != -52.7 || rep.WindDirDeg != 237 || rep.WindKt != 39 {
		t.Errorf("rep081 = %+v", rep)
	}
	f, _ := load(t, "rep081_handoff")
	got := ResolveMonthSeconds(rep.SecsInMonth, f.Received)
	if want := time.Date(2026, 10, 29, 17, 35, 51, 0, time.UTC); !got.Equal(want) {
		t.Errorf("time = %v, want %v", got, want)
	}

	// A MIAM without REP081 still yields the tail.
	_, r = load(t, "real_miam_t12")
	if r.Identity.Tail != "B-LRD" || r.Tree == nil {
		t.Errorf("t12: %+v", r.Identity)
	}
}

// Fixture 8.
func TestAFN(t *testing.T) {
	_, r := load(t, "afn_contact")
	if r.Kind != KindAFN || r.Label != "A0" || r.Dir != Uplink {
		t.Fatalf("%v %s %v", r.Kind, r.Label, r.Dir)
	}
	if r.Identity.Callsign != "CPA833" || r.Identity.Tail != "B-LRD" {
		t.Errorf("identity = %+v", r.Identity)
	}
	if r.Fields["contact_station"] != "OAKODYA" || r.Fields["gs_addr"] != "YVRE2YA" || r.Fields["afn"] != "contact_advisory" {
		t.Errorf("fields = %v", r.Fields)
	}
}

// The reply to a logon: FAK names the facility, FAR each application with its version and ground address.
func TestAFNAcknowledgement(t *testing.T) {
	r, err := Decode(Message{Label: "A0", Text: "/FUKJJYA.AFN/FMHACA025,.C-FNOH,C023C8,013352/FAK0,RJJJ/FARADS,0/FARATC,0,FUKJJYA9971"})
	if err != nil {
		t.Fatal(err)
	}
	apps, _ := r.Fields["apps"].([]AFNApp)
	if r.Kind != KindAFN || r.Fields["afn"] != "acknowledgement" || r.Fields["facility"] != "RJJJ" || len(apps) != 2 ||
		apps[1] != (AFNApp{Name: "ATC", Version: "0", Address: "FUKJJYA9971"}) || r.Fields["unparsed_segments"] != nil {
		t.Errorf("fields = %v", r.Fields)
	}
	if got, want := r.Summary, "AFN logon acknowledged by RJJJ: ADS-C, CPDLC, ACA025"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// Fixture 9.
func TestMedia(t *testing.T) {
	_, r := load(t, "media_established")
	if r.Kind != KindMedia || r.Fields["state"] != "established" || r.Fields["link"] != "satcom" || r.Fields["time_hhmmss"] != "124056" {
		t.Errorf("established: %v %v", r.Kind, r.Fields)
	}
	_, r = load(t, "media_lost")
	if r.Fields["state"] != "lost" || r.Fields["link"] != "VHF ACARS" || r.Fields["time_hhmmss"] != "193609" {
		t.Errorf("lost: %v", r.Fields)
	}
	_, r = load(t, "real_media_lost_prefixed") // 10-char prefix and trailing "/"
	if r.Fields["state"] != "lost" || r.Identity.Flight != "CX113" {
		t.Errorf("prefixed: %v %+v", r.Fields, r.Identity)
	}
}

// Fixture 10.
func TestDATIS(t *testing.T) {
	_, r := load(t, "datis_arrival")
	if r.Kind != KindDATIS || r.Label != "B9" || r.Fields["airport"] != "VHHH" || r.Fields["atis_type"] != "arrival" {
		t.Errorf("%v %v", r.Kind, r.Fields)
	}
	_, r = load(t, "real_datis_prefixed")
	if r.Fields["airport"] != "YBBN" || r.Identity.Flight != "CX113" {
		t.Errorf("prefixed: %v %+v", r.Fields, r.Identity)
	}
}

func TestRealCPDLC(t *testing.T) {
	for _, n := range []string{"real_cpdlc_prefixed", "real_cpdlc_cc1"} {
		_, r := load(t, n)
		if r.Tree == nil || r.Identity.Tail != "B-LRD" || r.Identity.Flight != "CX113" {
			t.Errorf("%s: %+v tree=%v", n, r.Identity, r.Tree != nil)
		}
	}
}

func TestResolveSecondsPastHour(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 28, h, m, s, 0, time.UTC) }
	cases := []struct {
		secs float64
		recv time.Time
		want time.Time
	}{
		{429, at(4, 7, 17), at(4, 7, 9)},
		{3590, at(5, 0, 5), at(4, 59, 50)},   // report just before the hour turned
		{429, at(4, 5, 0), at(4, 7, 9)},      // station clock ~2 min behind: stay in this hour
		{429, at(4, 1, 0), at(3, 7, 9)},      // far too early: previous hour
		{176, at(11, 59, 40), at(12, 2, 56)}, // logged 3 min before the report's own time, over the hour
		{3590, at(4, 59, 0), at(4, 59, 50)},  // ... and still this hour when that is the nearer
	}
	for _, c := range cases {
		if got := ResolveSecondsPastHour(c.secs, c.recv); !got.Equal(c.want) {
			t.Errorf("secs %v recv %v: got %v want %v", c.secs, c.recv, got, c.want)
		}
	}
}

func TestResolveMonthSecondsRollover(t *testing.T) {
	// 2482551 s is Oct 29 17:35:51. A message received early Nov 1 reporting it is from last month.
	recv := time.Date(2026, 11, 1, 0, 5, 0, 0, time.UTC)
	got := ResolveMonthSeconds(2482551, recv)
	if want := time.Date(2026, 10, 29, 17, 35, 51, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestPlausible(t *testing.T) {
	a := TimedPoint{47.4185, -122.3075, time.Date(2026, 10, 27, 19, 9, 30, 0, time.UTC)}
	b := TimedPoint{47.4185, -122.3075, a.T.Add(2 * 24 * time.Hour)}
	if !Plausible(a, b) {
		t.Error("same place two days apart should be plausible")
	}
	far := TimedPoint{35, 139, a.T.Add(10 * time.Minute)} // Seattle→Tokyo in 10 minutes
	if Plausible(a, far) {
		t.Error("Seattle→Tokyo in 10 min should be flagged")
	}
}

func TestSummariesAndPositions(t *testing.T) {
	f, r := load(t, "adsc_takeoff_sea")
	if r.Pos == nil || r.Pos.Basis != BasisHour || r.Pos.Lat < 47.41 || r.Pos.Lat > 47.42 {
		t.Fatalf("pos = %+v", r.Pos)
	}
	if got, want := r.Pos.At(f.Received), time.Date(2026, 10, 27, 19, 9, 30, 0, time.UTC); !got.Equal(want) {
		t.Errorf("At = %v, want %v", got, want)
	}
	if !strings.Contains(r.Summary, "ADS-C report 47.419N 122.308W") || !strings.Contains(r.Summary, "1868 ft") {
		t.Errorf("summary = %q", r.Summary)
	}
	if !strings.Contains(r.Text, "Basic report") {
		t.Errorf("text = %q", r.Text)
	}

	_, r = load(t, "adsc_periodic")
	if len(r.Route) != 3 || r.Route[0].Lat < 26.99 || r.Route[0].ETASec == nil || *r.Route[0].ETASec != 1088 {
		t.Fatalf("route = %+v", r.Route)
	}
	// next waypoint, the one after, then the flight computer's own projection of where it will be in 2220 s.
	if r.Route[0].Kind != "next" || r.Route[1].Kind != "next+1" || r.Route[2].Kind != "fixed" || *r.Route[2].ETASec != 2220 {
		t.Errorf("kinds = %q %q %q", r.Route[0].Kind, r.Route[1].Kind, r.Route[2].Kind)
	}
	near(t, "fixed lat", r.Route[2].Lat, 26.527, 1e-3)
	near(t, "fixed lon", r.Route[2].Lon, 147.257, 1e-3)

	_, r = load(t, "rep081_handoff")
	if r.Pos == nil || r.Pos.Basis != BasisMonth || *r.Pos.AltFt != 39000 {
		t.Fatalf("rep081 pos = %+v", r.Pos)
	}
	if !strings.HasPrefix(r.Summary, "REP081 VHHH→YSSY 16.919S 142.488E FL390") {
		t.Errorf("summary = %q", r.Summary)
	}

	want := map[string]string{
		"cpdlc_wilco":       "CPDLC downlink: WILCO",
		"afn_contact":       "AFN contact advisory: log on to OAKODYA, CPA833",
		"media_established": "Link established: satcom at 12:40:56Z (available: VHF ACARS, satcom)",
		"datis_arrival":     "D-ATIS arrival request for VHHH",
	}
	for name, w := range want {
		if _, r := load(t, name); r.Summary != w {
			t.Errorf("%s: summary = %q, want %q", name, r.Summary, w)
		}
	}
}

// Fixture 5 on the map: 19°10.3'N 115°52.3'E, FL370, 16:06Z.
func TestCPDLCPositionReport(t *testing.T) {
	f, r := load(t, "cpdlc_position")
	if r.Pos == nil || r.Pos.Basis != BasisDay {
		t.Fatalf("pos = %+v", r.Pos)
	}
	near(t, "lat", r.Pos.Lat, 19+10.3/60, 1e-6)
	near(t, "lon", r.Pos.Lon, 115+52.3/60, 1e-6)
	if r.Pos.AltFt == nil || *r.Pos.AltFt != 37000 {
		t.Errorf("alt = %v", r.Pos.AltFt)
	}
	if got, want := r.Pos.At(f.Received), time.Date(2026, 10, 28, 16, 6, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("At = %v, want %v", got, want)
	}
	// ... naming the waypoints the report carried: the one just passed, the next (with ETAs), and the one after.
	if r.Summary != "CPDLC position report 19.172N 115.872E 37000 ft \u2014 passed UNTUL 15:58Z, next SABNO 16:07Z, then DALRA" {
		t.Errorf("summary = %q", r.Summary)
	}
	// Other CPDLC messages carry no position.
	for _, n := range []string{"cpdlc_wilco", "real_cpdlc_cc1"} {
		if _, r := load(t, n); r.Pos != nil {
			t.Errorf("%s has a position: %+v", n, r.Pos)
		}
	}
}

func TestCPDLCPositionHemispheresAndFix(t *testing.T) {
	mk := func(cur map[string]any) map[string]any {
		return map[string]any{"arinc622": map[string]any{"cpdlc": map[string]any{"atc_downlink_msg": map[string]any{
			"header":                      map[string]any{"timestamp": map[string]any{"hour": 23.0, "min": 59.0, "sec": 30.0}},
			"atc_downlink_msg_element_id": map[string]any{"data": map[string]any{"pos_report": map[string]any{"pos_current": cur}}},
		}}}}
	}
	// South and west are negative; no time_at_pos_current, so the header time is used.
	ll := map[string]any{"data": map[string]any{"lat_lon": map[string]any{
		"lat": map[string]any{"deg": 33.0, "min": 30.0, "dir": "south"},
		"lon": map[string]any{"deg": 70.0, "min": 15.0, "dir": "west"}}}}
	p, fix := cpdlcPosition(mk(ll))
	if p == nil || fix != "" {
		t.Fatalf("p=%+v fix=%q", p, fix)
	}
	near(t, "lat", p.Lat, -33.5, 1e-9)
	near(t, "lon", p.Lon, -70.25, 1e-9)
	// Received just after midnight: 23:59:30 belongs to the previous day.
	if got, want := p.At(time.Date(2026, 10, 29, 0, 0, 10, 0, time.UTC)), time.Date(2026, 10, 28, 23, 59, 30, 0, time.UTC); !got.Equal(want) {
		t.Errorf("midnight rollover: %v, want %v", got, want)
	}
	// A report naming a fix gives no position but keeps the name.
	p, fix = cpdlcPosition(mk(map[string]any{"choice": "fixName", "data": map[string]any{"fix": "UNTUL"}}))
	if p != nil || fix != "UNTUL" {
		t.Errorf("fix-only: p=%+v fix=%q", p, fix)
	}
	if p, fix = cpdlcPosition(map[string]any{"arinc622": map[string]any{}}); p != nil || fix != "" {
		t.Errorf("not a position report: %+v %q", p, fix)
	}
}

// Real DAL1317 (N393DA) traffic, label H1.
func TestDeltaH1(t *testing.T) {
	// VDL2 sometimes drops the leading "/" of an ARINC 622 message.
	_, r := load(t, "dal_cpdlc_noslash_uplink")
	if r.Kind != KindCPDLC || !strings.HasPrefix(r.Body, "/USADCXA.AT1.") || r.Identity.Tail != "N393DA" || r.Tree == nil {
		t.Fatalf("no-slash CPDLC: %v %q %+v", r.Kind, r.Body, r.Identity)
	}
	// Direction unknown: it only parses as an uplink, so that is what we settle on.
	if r.Dir != Uplink || !strings.Contains(r.Summary, "uplink") {
		t.Errorf("dir = %v, summary %q", r.Dir, r.Summary)
	}
	if _, r = load(t, "dal_cpdlc_cc1"); r.Kind != KindCPDLC || r.Dir != Downlink || r.Tree == nil {
		t.Errorf("CC1: %v dir %v", r.Kind, r.Dir)
	}

	// An explicit direction is respected rather than second-guessed.
	f, _ := load(t, "dal_cpdlc_noslash_uplink")
	r, _ = Decode(Message{Label: f.Label, Dir: Downlink, Text: f.Text})
	if r.Dir != Downlink {
		t.Errorf("explicit downlink overridden: %v", r.Dir)
	}

	// FMS position report: 34°44.7'N 119°19.5'W, FL250, heading for MDOTS.
	f, r = load(t, "dal_fms_posn")
	if r.Kind != KindFMS || r.Pos == nil {
		t.Fatalf("POSN: %v %+v", r.Kind, r.Pos)
	}
	near(t, "lat", r.Pos.Lat, 34+44.7/60, 1e-9)
	near(t, "lon", r.Pos.Lon, -(119 + 19.5/60), 1e-9)
	if r.Pos.AltFt == nil || *r.Pos.AltFt != 25000 || r.Fields["next_waypoint"] != "MDOTS" || r.Fields["waypoint"] != "MUPTT" {
		t.Errorf("alt/fields: %v %v", r.Pos.AltFt, r.Fields)
	}
	if !r.Pos.At(f.Received).Equal(f.Received) {
		t.Error("a POSN report should be placed at its receipt time")
	}
	if r.Fields["wpt_time"] != "232718" {
		t.Errorf("wpt_time = %v", r.Fields["wpt_time"])
	}
	if r.Summary != "FMS position report 34.745N 119.325W FL250, next MDOTS" {
		t.Errorf("summary = %q", r.Summary)
	}

	// Airline engine data: no decoder, and the summary says so without claiming more.
	_, r = load(t, "dal_engine_data")
	if r.Kind != KindUnknown || r.Summary != "Delta aircraft data report (label H1, not decoded)" || r.Pos != nil {
		t.Errorf("engine data: %v %q", r.Kind, r.Summary)
	}
}

func TestFMSPositionSouthAndEast(t *testing.T) {
	r, _ := Decode(Message{Label: "H1", Text: "POSS33510E151100,ABCDE,010203,380,FGHIJ,010304,KLMNO,M55,270090,1234"})
	if r.Kind != KindFMS || r.Pos == nil {
		t.Fatalf("%v %+v", r.Kind, r.Pos)
	}
	near(t, "lat", r.Pos.Lat, -(33 + 51.0/60), 1e-9)
	near(t, "lon", r.Pos.Lon, 151+10.0/60, 1e-9)
	// Other things that merely start with "POS" are not position reports.
	if r, _ := Decode(Message{Label: "H1", Text: "POSITION OK"}); r.Kind != KindUnknown {
		t.Errorf("POSITION OK: %v", r.Kind)
	}
}

func wxOf(t *testing.T, r Result) (dir, kt, temp float64) {
	t.Helper()
	if r.Wx == nil || r.Wx.WindDirDeg == nil || r.Wx.WindKt == nil || r.Wx.TempC == nil {
		t.Fatalf("incomplete weather: %+v", r.Wx)
	}
	return *r.Wx.WindDirDeg, *r.Wx.WindKt, *r.Wx.TempC
}

// Wind (from, true) and temperature come from every kind of position report, ground speed and
// track from the ones that give them.
func TestWeatherAndMotion(t *testing.T) {
	_, r := load(t, "adsc_meteo")
	d, k, c := wxOf(t, r)
	near(t, "wind dir", d, 235.5, 0.1)
	near(t, "wind kt", k, 34.5, 0.01)
	near(t, "temp", c, -52.5, 0.01)
	near(t, "gs", *r.GS, 483, 0.5)
	near(t, "trk", *r.Track, 153.6, 0.1)

	_, r = load(t, "adsc_periodic") // no meteo group, but speed and track
	if r.Wx != nil || r.GS == nil || r.Track == nil {
		t.Errorf("adsc_periodic: wx=%+v gs=%v trk=%v", r.Wx, r.GS, r.Track)
	}
	near(t, "gs", *r.GS, 472.5, 0.01)
	near(t, "trk", *r.Track, 256.2, 0.05)

	_, r = load(t, "cpdlc_position")
	d, k, c = wxOf(t, r)
	near(t, "wind dir", d, 70, 0)
	near(t, "wind kt", k, 50, 0)
	near(t, "temp", c, -45, 0)
	near(t, "gs", *r.GS, 520, 0)
	near(t, "trk", *r.Track, 184, 0)

	_, r = load(t, "rep081_handoff")
	d, k, c = wxOf(t, r)
	near(t, "wind dir", d, 237, 0)
	near(t, "wind kt", k, 39, 0)
	near(t, "temp", c, -52.7, 1e-9)

	_, r = load(t, "dal_fms_posn") // ...,M27,261027,...
	d, k, c = wxOf(t, r)
	near(t, "wind dir", d, 261, 0)
	near(t, "wind kt", k, 27, 0)
	near(t, "temp", c, -27, 0)

	// Messages with no weather say so.
	for _, n := range []string{"cpdlc_wilco", "afn_contact", "media_established", "datis_arrival"} {
		if _, r := load(t, n); r.Wx != nil || r.GS != nil {
			t.Errorf("%s: wx=%+v gs=%v", n, r.Wx, r.GS)
		}
	}
}

func TestFMSWeatherParsing(t *testing.T) {
	cases := []struct {
		temp, wind string
		dir, kt, c *float64
	}{
		{"M51", "264080", fp(264), fp(80), fp(-51)},
		{"P05", "090012", fp(90), fp(12), fp(5)},
		{"M99", "264080", fp(264), fp(80), nil}, // -99 C is not an atmosphere
		{"M51", "264999", nil, nil, fp(-51)},    // 999 kt is not a wind
		{"M51", "999080", nil, nil, fp(-51)},    // neither is 999 degrees
		{"X12", "26408", nil, nil, nil},         // garbage
		{"", "", nil, nil, nil},
	}
	for _, c := range cases {
		w := fmsWeather(c.temp, c.wind)
		if c.dir == nil && c.kt == nil && c.c == nil {
			if w != nil {
				t.Errorf("%q %q: %+v", c.temp, c.wind, w)
			}
			continue
		}
		if w == nil || !eq(w.WindDirDeg, c.dir) || !eq(w.WindKt, c.kt) || !eq(w.TempC, c.c) {
			t.Errorf("%q %q: got %+v", c.temp, c.wind, w)
		}
	}
}

func fp(v float64) *float64 { return &v }
func eq(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// A CPDLC position report names the waypoint it has just passed (with when), the next one (with
// its ETA) and the one after (with neither).
func TestCPDLCNamedFixes(t *testing.T) {
	_, r := load(t, "cpdlc_position")
	if len(r.Fixes) != 3 {
		t.Fatalf("fixes = %+v", r.Fixes)
	}
	want := []struct {
		name, role string
		secs, alt  *float64
	}{
		{"UNTUL", "passed", fp(15*3600 + 58*60), fp(34300)},
		{"SABNO", "next", fp(16*3600 + 7*60), nil},
		{"DALRA", "then", nil, nil},
	}
	for i, w := range want {
		g := r.Fixes[i]
		if g.Name != w.name || g.Role != w.role || !eq(g.Secs, w.secs) || !eq(g.AltFt, w.alt) {
			t.Errorf("fix %d = %+v, want %+v", i, g, w)
		}
	}
	// Other CPDLC messages name no fixes.
	if _, r := load(t, "cpdlc_wilco"); len(r.Fixes) != 0 {
		t.Errorf("wilco: %+v", r.Fixes)
	}
}

func TestNearestTimeOfDay(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }
	cases := []struct {
		secs float64
		ref  time.Time
		want time.Time
	}{
		{16*3600 + 7*60, at(28, 16, 6), at(28, 16, 7)},   // an ETA a minute ahead
		{15*3600 + 58*60, at(28, 16, 6), at(28, 15, 58)}, // a time just passed
		{0*3600 + 5*60, at(28, 23, 55), at(29, 0, 5)},    // an ETA just after midnight
		{23*3600 + 50*60, at(29, 0, 4), at(28, 23, 50)},  // a time just before midnight
	}
	for _, c := range cases {
		if got := NearestTimeOfDay(c.secs, c.ref); !got.Equal(c.want) {
			t.Errorf("%v around %v = %v, want %v", c.secs, c.ref, got, c.want)
		}
	}
}

func TestFixNames(t *testing.T) {
	secs := func(h, m int) *float64 { v := float64(h*3600 + m*60); return &v }
	got := fixNames([]FixRef{{Name: "OLGAN", Role: "passed", Secs: secs(18, 26)}, {Name: "KADAB", Role: "next", Secs: secs(18, 55)}, {Name: "KAPPU", Role: "then"}})
	if got != " \u2014 passed OLGAN 18:26Z, next KADAB 18:55Z, then KAPPU" {
		t.Errorf("got %q", got)
	}
	if fixNames(nil) != "" {
		t.Error("no fixes, no suffix")
	}
	// a time past midnight stays on the clock
	if got := fixNames([]FixRef{{Name: "X", Role: "next", Secs: secs(24, 5)}}); got != " \u2014 next X 00:05Z" {
		t.Errorf("got %q", got)
	}
}

func TestACMFReport(t *testing.T) {
	// The first block of a 787 ACMF report: header plus two fixes. The newest fix is the message's position.
	f, r := load(t, "acmf_header")
	if r.Kind != KindACMF || r.Label != "H1" || r.Pos == nil {
		t.Fatalf("header block: %v %q %+v", r.Kind, r.Label, r.Pos)
	}
	if r.Identity.Tail != "N24993" || r.Identity.Callsign != "UAL143" {
		t.Errorf("identity = %+v", r.Identity)
	}
	if r.Fields["origin"] != "KDEN" || r.Fields["dest"] != "RJAA" || r.Fields["elapsed_s"] != 16591 || r.Fields["report_time"] != "225335" {
		t.Errorf("fields = %v", r.Fields)
	}
	near(t, "lat", r.Pos.Lat, 58+18.3/60, 1e-9)
	near(t, "lon", r.Pos.Lon, -(145 + 55.3/60), 1e-9)
	if r.Pos.AltFt == nil || *r.Pos.AltFt != 36002 {
		t.Errorf("alt = %v", r.Pos.AltFt)
	}
	if want := time.Date(2026, 10, 30, 22, 41, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("fix time = %v, want %v", r.Pos.At(f.Received), want)
	}
	if r.Wx == nil || *r.Wx.WindDirDeg != 211 || *r.Wx.WindKt != 81 || *r.Wx.TempC != -49.6 {
		t.Errorf("wx = %+v", r.Wx)
	}
	if pts, _ := r.Fields["points"].([]ACMFPoint); len(pts) != 2 || pts[0].Phase != "ER" {
		t.Errorf("points = %+v", r.Fields["points"])
	}
	if want := "ACMF report KDEN→RJAA 58.305N 145.922W FL360 en route, 2 fixes"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}

	// A continuation block has no header, and is cut off mid-line: only whole lines count.
	f, r = load(t, "acmf_continuation")
	if r.Kind != KindACMF || r.Pos == nil || r.Identity.Tail != "" {
		t.Fatalf("continuation: %v %+v %+v", r.Kind, r.Pos, r.Identity)
	}
	if pts, _ := r.Fields["points"].([]ACMFPoint); len(pts) != 3 {
		t.Errorf("points = %d, want 3", len(pts))
	}
	near(t, "lon", r.Pos.Lon, -(4 + 12.4/60), 1e-9)
	if want := time.Date(2026, 10, 27, 12, 39, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("fix time = %v, want %v", r.Pos.At(f.Received), want)
	}
}

func TestAOCPositionReport(t *testing.T) {
	// ZIPAIR flight ZG1, Honolulu to Narita, heading west: the report names where it is going.
	f, r := load(t, "aoc83_zipair")
	if r.Kind != KindAOC || r.Label != "83" || r.Pos == nil {
		t.Fatalf("zipair: %v %q %+v", r.Kind, r.Label, r.Pos)
	}
	if r.Fields["origin"] != "PHNL" || r.Fields["dest"] != "RJAA" || r.Identity.FlightID != "ZG0001" || r.Identity.Flight != "ZG1" {
		t.Errorf("fields %v identity %+v", r.Fields, r.Identity)
	}
	near(t, "lat", r.Pos.Lat, 28.71, 1e-9)
	near(t, "lon", r.Pos.Lon, 173.77, 1e-9)
	if r.Pos.AltFt == nil || *r.Pos.AltFt != 40000 || r.Fields["tailwind_kt"] != -84.1 || r.Fields["wind_dir"] != 258.0 {
		t.Errorf("alt %v fields %v", r.Pos.AltFt, r.Fields)
	}
	if want := time.Date(2026, 10, 30, 23, 14, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("fix time = %v, want %v", r.Pos.At(f.Received), want)
	}
	if want := "Airline position report PHNL→RJAA 28.710N 173.770E FL400"; r.Summary != want {
		t.Errorf("summary = %q", r.Summary)
	}

	// No message number or flight in front; the sign of the longitude is followed by spaces.
	_, r = load(t, "aoc83_united_west")
	if r.Kind != KindAOC || r.Pos == nil || r.Fields["origin"] != "KDEN" || r.Fields["dest"] != "LIRF" || r.Identity.Flight != "" {
		t.Fatalf("united: %v %+v %v", r.Kind, r.Pos, r.Fields)
	}
	near(t, "lon", r.Pos.Lon, -4.15, 1e-9)
	if r.Fields["tailwind_kt"] != 130.1 {
		t.Errorf("tailwind = %v", r.Fields["tailwind_kt"])
	}
}

func TestPosReport(t *testing.T) {
	// Delivered on 31 Oct at 05:02, but dated 28 Oct 10:29: it is placed at its own time.
	f, r := load(t, "pos_late")
	if r.Kind != KindPOS || r.Pos == nil || r.Label != "H1" {
		t.Fatalf("late: %v %+v", r.Kind, r.Pos)
	}
	near(t, "lat", r.Pos.Lat, 39+13.0/60, 1e-9)
	near(t, "lon", r.Pos.Lon, -(123 + 15.0/60), 1e-9)
	if r.Pos.AltFt == nil || *r.Pos.AltFt != 38000 {
		t.Errorf("alt = %v", r.Pos.AltFt)
	}
	if want := time.Date(2026, 10, 28, 10, 29, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("placed at %v, want %v", r.Pos.At(f.Received), want)
	}
	if r.Wx == nil || *r.Wx.WindDirDeg != 278 || *r.Wx.WindKt != 62 || *r.Wx.TempC != -60 {
		t.Errorf("wx = %+v", r.Wx)
	}
	if r.Fields["mach"] != 0.848 || r.Fields["value"] != 1298 {
		t.Errorf("fields = %v", r.Fields)
	}
	if want := "Position report 39.217N 123.250W FL380, M0.848"; r.Summary != want {
		t.Errorf("summary = %q", r.Summary)
	}

	// Sent on time, it is placed at its own time too, which is the arrival time to within the minute.
	f, r = load(t, "pos_ontime")
	if want := time.Date(2026, 10, 30, 10, 14, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("on time: %v", r.Pos.At(f.Received))
	}
	// Climbing, warm: plus and minus temperatures and low flight levels read as numbers.
	_, r = load(t, "pos_climb")
	if r.Pos == nil || *r.Pos.AltFt != 22200 || *r.Wx.TempC != -20 || r.Fields["mach"] != 0.727 {
		t.Errorf("climb: %+v %+v %v", r.Pos, r.Wx, r.Fields)
	}
	// Southern and eastern hemispheres, and a plus temperature.
	res, err := Decode(Message{Label: "H1", Text: "*POS\r\n01020304S3328E11846081236010372P1000368"})
	if err != nil || res.Kind != KindPOS || res.Pos == nil || res.Pos.Lat > -33.4 || res.Pos.Lon < 118.7 || *res.Wx.TempC != 10 {
		t.Errorf("S/E: %v %+v %+v", err, res.Pos, res.Wx)
	}
	// An impossible date or position is not decoded as one.
	for _, bad := range []string{"*POS\r\n13281029N3913W12315380278062848M6021298", "*POS\r\n10321029N3913W12315380278062848M6021298",
		"*POS\r\n10282529N3913W12315380278062848M6021298", "*POS\r\n10281029N3963W12315380278062848M6021298"} {
		if r, _ := Decode(Message{Label: "H1", Text: bad}); r.Pos != nil {
			t.Errorf("%q decoded to %+v", bad, r.Pos)
		}
	}
}

func TestResolveMonthDay(t *testing.T) {
	at := func(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		n    int
		ref  time.Time
		want time.Time
	}{
		{10281029, at(2026, 10, 31, 5, 3), at(2026, 10, 28, 10, 29)},   // three days late
		{12312350, at(2027, 1, 1, 0, 10), at(2026, 12, 31, 23, 50)},    // over the new year
		{1010005, at(2026, 12, 31, 23, 55), at(2027, 1, 1, 0, 5)},      // and the other way
		{2290000, at(2028, 3, 1, 1, 0), at(2028, 2, 29, 0, 0)},         // a leap day
		{2290000, at(2026, 3, 1, 1, 0), at(2026, 3, 1, 1, 0)},          // not in 2026: placed at the arrival
		{10301022, at(2026, 10, 30, 10, 22), at(2026, 10, 30, 10, 22)}, // on time
	} {
		if got := ResolveMonthDay(tc.n, tc.ref); !got.Equal(tc.want) {
			t.Errorf("ResolveMonthDay(%d, %v) = %v, want %v", tc.n, tc.ref, got, tc.want)
		}
	}
}

func TestPOSRPT(t *testing.T) {
	// DAL39 off Point Conception, delivered five and a half hours after the time it carries.
	f, r := load(t, "posrpt_dal39_late")
	if r.Kind != KindPOSRPT || r.Label != "80" || r.Pos == nil {
		t.Fatalf("kind %v label %q pos %+v", r.Kind, r.Label, r.Pos)
	}
	near(t, "lat", r.Pos.Lat, 34+41.4/60, 1e-9)
	near(t, "lon", r.Pos.Lon, -(119 + 59.8/60), 1e-9)
	if r.Pos.AltFt == nil || *r.Pos.AltFt != 25200 {
		t.Errorf("alt = %v", r.Pos.AltFt)
	}
	// 19:41 on the 30th, the day the flight is dated, though it arrived at 01:10 on the 31st.
	if want := time.Date(2026, 10, 30, 19, 41, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("placed at %v, want %v", r.Pos.At(f.Received), want)
	}
	if r.Identity.Tail != "N501DN" || r.Fields["origin"] != "KLAX" || r.Fields["dest"] != "ZSPD" || r.Fields["eta"] != "0925" {
		t.Errorf("identity %+v fields %v", r.Identity, r.Fields)
	}
	if r.Wx == nil || *r.Wx.WindDirDeg != 239 || *r.Wx.WindKt != 32 || *r.Wx.TempC != -29 || r.Fields["mach"] != 0.759 || r.Fields["tas"] != 462 || r.Fields["fob"] != 2022 {
		t.Errorf("wx %+v fields %v", r.Wx, r.Fields)
	}
	if want := "Position report KLAX→ZSPD 34.690N 119.997W FL252, M0.759"; r.Summary != want {
		t.Errorf("summary = %q", r.Summary)
	}
	// Just after takeoff (2,100 ft, positive OAT with a leading zero).
	_, r = load(t, "posrpt_dal39_takeoff")
	if *r.Pos.AltFt != 2100 || *r.Wx.TempC != 14 || r.Fields["fob"] != 2121 {
		t.Errorf("takeoff: %v %+v %v", *r.Pos.AltFt, r.Wx, r.Fields)
	}
	// A message number and flight in front, tabs before the lines.
	_, r = load(t, "posrpt_prefixed")
	if r.Kind != KindPOSRPT || r.Identity.FlightID != "DL0170" || r.Identity.Flight != "DL170" || r.Identity.Tail != "N515DN" || r.Pos.Lon < 149 {
		t.Errorf("prefixed: %v %+v %+v", r.Kind, r.Identity, r.Pos)
	}
	// Fiji: a hyphenated tail, a wind direction below zero (-142 is 218) and no ETA.
	_, r = load(t, "posrpt_fiji_negwind")
	if r.Kind != KindPOSRPT || r.Identity.Tail != "DQ-FAM" || *r.Wx.WindDirDeg < 0 || *r.Wx.WindDirDeg > 360 {
		t.Errorf("fiji: %v %+v %+v", r.Kind, r.Identity, r.Wx)
	}
	res, _ := Decode(Message{Label: "80", Text: "3N01 POSRPT 0811/26 KLAX/NFFN .DQ-FAM\n/POS S03020W165549/ALT 400/MCH 845/FOB 0207\n/TME 1447/WND -142 16/OAT -53/TAS 489/ETA 1739"})
	if res.Pos == nil || *res.Wx.WindDirDeg != 218 || res.Pos.Lat > -3 || res.Pos.Lon > -165 {
		t.Errorf("negative direction: %+v %+v", res.Pos, res.Wx)
	}
	for _, name := range []string{"posrpt_eta_dashes", "posrpt_eta_blank"} {
		if _, r = load(t, name); r.Kind != KindPOSRPT || r.Pos == nil || r.Fields["eta"] != nil {
			t.Errorf("%s: %v %+v %v", name, r.Kind, r.Pos, r.Fields)
		}
	}
}

func TestResolveDayTime(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }
	sec := func(h, m int) float64 { return float64(h*3600 + m*60) }
	for _, tc := range []struct {
		name string
		day  int
		s    float64
		recv time.Time
		want time.Time
	}{
		{"on time", 30, sec(14, 54), at(30, 14, 51), at(30, 14, 54)}, // the station is three minutes early
		{"an hour late", 30, sec(13, 54), at(30, 14, 52), at(30, 13, 54)},
		{"5 h late, over midnight", 30, sec(19, 41), at(31, 1, 10), at(30, 19, 41)},
		{"flight dated the 30th, report after midnight UTC", 30, sec(2, 0), at(31, 2, 3), at(31, 2, 0)},
		{"7 h late, same day", 28, sec(10, 20), at(28, 17, 30), at(28, 10, 20)},
		{"a flight dated the 28th, reporting late in the evening of the 29th", 28, sec(23, 0), at(30, 3, 0), at(29, 23, 0)},
		{"no matching day: the latest time of day", 15, sec(10, 20), at(30, 12, 0), at(30, 10, 20)},
		{"timed just after midnight, logged just before", 31, sec(0, 2), at(30, 23, 59), at(31, 0, 2)},
	} {
		if got := ResolveDayTime(tc.day, tc.s, tc.recv); !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// UPS's satellite header in front of ADS-C, CPDLC and *POS: the message is what it would have been without it.
func TestUPSSatcomWrapper(t *testing.T) {
	// A downlink ADS-C report from the Pacific, flight UPS6059.
	f, r := load(t, "ups_adsc_down")
	if r.Kind != KindADSC || r.Label != "B6" || r.Dir != Downlink || r.Tree == nil {
		t.Fatalf("adsc downlink: %v %q %v tree %v", r.Kind, r.Label, r.Dir, r.Tree != nil)
	}
	if r.Identity.Tail != "N575UP" || r.Identity.FlightID != "UP6059" || r.Identity.Flight != "UP6059" || r.Identity.MsgNum != "F78A" {
		t.Errorf("identity = %+v", r.Identity)
	}
	if r.Pos == nil {
		t.Fatalf("no position in the report: %+v", r.Fields)
	}
	if got := r.Pos.At(f.Received); got.Sub(f.Received).Abs() > 20*time.Minute {
		t.Errorf("placed at %v, received %v", got, f.Received)
	}
	if !strings.HasPrefix(r.Summary, "ADS-C report ") {
		t.Errorf("summary = %q", r.Summary)
	}

	// CPDLC downlink.
	_, r = load(t, "ups_cpdlc_down")
	if r.Kind != KindCPDLC || r.Label != "BA" || r.Dir != Downlink || r.Tree == nil || r.Identity.Tail != "N575UP" || r.Identity.FlightID != "UP6059" {
		t.Errorf("cpdlc downlink: %v %q %v %+v", r.Kind, r.Label, r.Dir, r.Identity)
	}

	// The *POS report behind "#DFB": a tab after the line break, and the flight from the header.
	f, r = load(t, "ups_pos_down")
	if r.Kind != KindPOS || r.Pos == nil || r.Identity.FlightID != "UP6059" {
		t.Fatalf("pos: %v %+v %+v", r.Kind, r.Pos, r.Identity)
	}
	near(t, "lat", r.Pos.Lat, 54+16.0/60, 1e-9)
	near(t, "lon", r.Pos.Lon, 175+32.0/60, 1e-9)
	if want := time.Date(2026, 10, 25, 15, 52, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("pos time = %v, want %v", r.Pos.At(f.Received), want)
	}

	// Uplinks, behind "- #MD": a periodic ADS-C contract request, and a CPDLC message.
	_, r = load(t, "ups_adsc_up")
	if r.Kind != KindADSC || r.Label != "A6" || r.Dir != Uplink || !strings.Contains(r.Text, "contract request") {
		t.Errorf("adsc uplink: %v %q %v %q", r.Kind, r.Label, r.Dir, r.Text)
	}
	_, r = load(t, "ups_adsc_up_fuk")
	if r.Kind != KindADSC || !strings.Contains(r.Text, "Reporting interval: 704") {
		t.Errorf("fukuoka contract: %v %q", r.Kind, r.Text)
	}
	_, r = load(t, "ups_cpdlc_up")
	if r.Kind != KindCPDLC || r.Label != "AA" || r.Dir != Uplink || !strings.Contains(r.Text, "CPDLC Uplink") || r.Identity.Tail != "N575UP" {
		t.Errorf("cpdlc uplink: %v %q %v %q %+v", r.Kind, r.Label, r.Dir, r.Text, r.Identity)
	}

	// An unwrapped message is unaffected, and something that only looks a little like a wrapper is left alone.
	for _, s := range []string{"- #MDREQPER9CB5", "plain text #M1B/ not a label"} {
		if r, _ := Decode(Message{Label: "H1", Text: s}); r.Kind != KindUnknown && r.Kind != KindText { // nothing but text
			t.Errorf("%q decoded as %v", s, r.Kind)
		}
	}
}

func TestChinaCargoPOSA1(t *testing.T) {
	f, r := load(t, "ck_posa1_satcom")
	if r.Kind != KindPOSA1 || r.Label != "16" || r.Pos == nil {
		t.Fatalf("kind %q label %q pos %v", r.Kind, r.Label, r.Pos)
	}
	near(t, "lat", r.Pos.Lat, 59.929, 1e-6)
	near(t, "lon", r.Pos.Lon, 149.881, 1e-6)
	near(t, "alt", *r.Pos.AltFt, 36000, 1)
	near(t, "temp", *r.Wx.TempC, -50, 0)
	if r.Fields["last_wpt"] != "DERUD" || r.Fields["next_wpt"] != "OVGAR" {
		t.Errorf("fields %v", r.Fields)
	}
	// Received 01:21:11, between DERUD at 01:11:41 and OVGAR at 01:36:35: placed at receipt.
	if got := r.Pos.At(f.Received); !got.Equal(f.Received) {
		t.Errorf("on time: placed at %v, want receipt %v", got, f.Received)
	}

	// Delivered after the window closed: the middle of it.
	f, r = load(t, "ck_posa1_late")
	near(t, "lon", r.Pos.Lon, -118.539, 1e-6)
	want := time.Date(2026, 10, 30, 17, 11, 46, 0, time.UTC).Add(time.Duration(((17*3600 + 13*60 + 25) - (17*3600 + 11*60 + 46)) * 1e9 / 2))
	if got := r.Pos.At(f.Received); !got.Equal(want) {
		t.Errorf("late: placed at %v, want %v", got, want)
	}

	// No waypoints (on the ground): placed at receipt.
	f, r = load(t, "ck_posa1_nowpt")
	if r.Pos == nil || r.Pos.Basis != BasisReceipt || !r.Pos.At(f.Received).Equal(f.Received) {
		t.Errorf("no waypoints: %+v", r.Pos)
	}

	_, r = load(t, "ck_posa1_europe")
	near(t, "padded lon", r.Pos.Lon, 9.1, 1e-6)
	// A coordinate for the last waypoint looks like an ACMF waypoint: it is still this report.
	_, r = load(t, "ck_posa1_latlonwpt")
	if r.Kind != KindPOSA1 {
		t.Errorf("coordinate waypoints: kind %q", r.Kind)
	}
	if r.Pos == nil || r.Fields["last_wpt"] != "N41E160" {
		t.Errorf("coordinate waypoints: %+v %v", r.Pos, r.Fields)
	}
}

func TestChinaCargoSB(t *testing.T) {
	f, r := load(t, "ck_sb_vdl")
	if r.Kind != KindSBPOS || r.Pos == nil {
		t.Fatalf("kind %q pos %v", r.Kind, r.Pos)
	}
	near(t, "lat", r.Pos.Lat, 62.28, 1e-6)
	near(t, "lon", r.Pos.Lon, -153.346, 1e-6)
	near(t, "alt", *r.Pos.AltFt, 35997, 0)
	near(t, "temp", *r.Wx.TempC, -42, 0)
	near(t, "wind dir", *r.Wx.WindDirDeg, 196, 0)
	near(t, "wind kt", *r.Wx.WindKt, 82, 0)
	if got, want := r.Pos.At(f.Received), time.Date(2026, 10, 30, 21, 54, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("time %v, want %v", got, want)
	}
	if r.Fields["origin"] != "KLAX" || r.Fields["dest"] != "ZSPD" {
		t.Errorf("fields %v", r.Fields)
	}

	// Behind the satellite header; the clock runs 3 minutes ahead of the receiving time.
	f, r = load(t, "ck_sb_satcom")
	if r.Kind != KindSBPOS || r.Pos == nil {
		t.Fatalf("satcom: kind %q pos %v", r.Kind, r.Pos)
	}
	near(t, "lon", r.Pos.Lon, 139.994, 1e-6)
	if got, want := r.Pos.At(f.Received), time.Date(2026, 10, 31, 2, 25, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("satcom time %v, want %v", got, want)
	}

	_, r = load(t, "ck_sb_approach")
	near(t, "lon", r.Pos.Lon, 8.95, 1e-6)
	near(t, "alt", *r.Pos.AltFt, 4007, 0)
	f, r = load(t, "ck_sb_late")
	if got := r.Pos.At(f.Received); got.Hour() != 14 || got.Minute() != 18 {
		t.Errorf("late time %v", got)
	}
}

func TestChinaCargoVariants(t *testing.T) {
	f, r := load(t, "ck_posa1_ground") // a runway for a waypoint, dashes for what is not known
	if r.Kind != KindPOSA1 || r.Pos == nil || r.Fields["last_wpt"] != "RW25L" {
		t.Fatalf("ground: %q %v %v", r.Kind, r.Pos, r.Fields)
	}
	if _, ok := r.Fields["wind_kt"]; !ok {
		t.Log("wind is the third value on the ground, dashes")
	}
	_ = f
	_, r = load(t, "ck_sb_sat_nohdr") // satellite header with no message number
	if r.Kind != KindSBPOS || r.Pos == nil {
		t.Errorf("no number: %q %v", r.Kind, r.Pos)
	}
	_, r = load(t, "ck_sb_short_seq") // two digit number after the airports
	if r.Kind != KindSBPOS || r.Pos == nil || r.Fields["dest"] != "KJFK" {
		t.Errorf("short number: %q %v", r.Kind, r.Fields)
	}
}

// A350 MIAM reports: the REP081 position in a compressed message, the header of the other ACMS reports, the XML
// maintenance report, the takeoff report and the acknowledgements.
func TestA350Reports(t *testing.T) {
	// REP081 with several samples (the newer software): the newest is the position, the others are kept
	_, r := load(t, "real_rep081_edr_3_samples")
	rep, ok := r.Fields["rep081"].(REP081Report)
	if !ok || r.Pos == nil || len(rep.Samples) < 2 {
		t.Fatalf("rep081 = %+v pos = %+v", rep, r.Pos)
	}
	if rep.Origin != "VHHH" || rep.Dest != "EGCC" || r.Identity.Tail == "" {
		t.Errorf("rep081 = %+v tail %q", rep, r.Identity.Tail)
	}
	for i := 1; i < len(rep.Samples); i++ {
		if rep.Samples[i].SecsInMonth <= rep.Samples[i-1].SecsInMonth {
			t.Errorf("samples not in time order: %+v", rep.Samples)
		}
	}
	if last := rep.Samples[len(rep.Samples)-1]; r.Pos.Lat != last.Lat || r.Pos.Secs != float64(last.SecsInMonth) {
		t.Errorf("the position is not the newest sample: %+v vs %+v", r.Pos, last)
	}
	near(t, "first sample lat", rep.Samples[0].Lat, 52.724, 1e-3)
	near(t, "first sample lon", rep.Samples[0].Lon, 4.299, 1e-3)
	if !strings.Contains(r.Summary, "REP081 VHHH→EGCC") || !strings.Contains(r.Summary, "newest of") {
		t.Errorf("summary = %q", r.Summary)
	}
	// ... and the same report as the single-sample form in a compressed message
	if _, r = load(t, "real_rep081_edr_sq"); r.Pos == nil || r.Fields["rep081"].(REP081Report).Dest != "WSSS" {
		t.Errorf("SQ rep081 pos = %+v", r.Pos)
	}

	// another A350 report: no position, but the aircraft, flight, route and what raised it
	_, r = load(t, "real_a350_rep216")
	a, ok := r.Fields["a350"].(A350Report)
	if !ok || r.Pos != nil {
		t.Fatalf("a350 = %+v pos = %+v", a, r.Pos)
	}
	if a.Number != "216" || a.Tail != "9V-SGC" || a.Callsign != "SIA23" || a.Origin != "KJFK" || a.Dest != "WSSS" ||
		a.Trigger != "INTEGRATED REFUEL PANEL DOOR OPEN" || !a.Time.Equal(time.Date(2026, 10, 26, 3, 28, 14, 0, time.UTC)) {
		t.Errorf("a350 = %+v", a)
	}
	if r.Identity.Callsign != "SIA23" || r.Identity.Tail != "9V-SGC" {
		t.Errorf("identity = %+v", r.Identity)
	}
	if want := "A350 report 216: INTEGRATED REFUEL PANEL DOOR OPEN (SIA23 KJFK→WSSS)"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}

	// the XML maintenance report
	_, r = load(t, "real_a350_rtr_xml")
	x, ok := r.Fields["rtr"].(RTRReport)
	if !ok || x.Tail != "DQ-FAM" || x.Callsign != "FJI910" || x.Origin != "YSSY" || x.Dest != "NFFN" || !strings.Contains(x.Message, "Lavatory water faucet") {
		t.Fatalf("rtr = %+v", x)
	}
	if !strings.HasPrefix(r.Summary, "A350 maintenance report: ") || !strings.HasSuffix(r.Summary, "(FJI910 YSSY→NFFN)") {
		t.Errorf("summary = %q", r.Summary)
	}

	// the takeoff report
	_, r = load(t, "real_takeoff_report")
	tk, ok := r.Fields["takeoff"].(TakeoffReport)
	if !ok || tk.Origin != "KEWR" || tk.Dest != "WSSS" || tk.Tail != "9V-SGC" || tk.Out != "1539" || tk.Off != "1556" || tk.ETA != "1017" || tk.Runway != "22R" || tk.TOWkg != 273552 {
		t.Fatalf("takeoff = %+v", tk)
	}
	if want := "Takeoff report KEWR→WSSS at 15:56Z, runway 22R, ETA 10:17Z"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}

	// an acknowledgement PDU has no body, and so no "|": it used to be read as a 10 character prefix
	_, r = load(t, "real_miam_ack")
	if r.Kind != KindMIAM || r.Tree == nil || r.Prefix != "" || r.Identity.Tail != "B-LRM" || !strings.HasPrefix(r.Summary, "MIAM acknowledgement of message ") {
		t.Errorf("ack: kind %v prefix %q tail %q summary %q", r.Kind, r.Prefix, r.Identity.Tail, r.Summary)
	}

	// headers that say nothing are not taken for a route or a flight
	h, ok := ParseA350("A350,000001,1,1,TB000000/REP048,01,02;H01,048,01,02,5100,00024,.N515DN,3,0,25,10,26,06,47,36,010/H02,---------,----------,S0285,S0685,CDAU041024/H03,RAT Shedding/")
	if !ok || h.Origin != "" || h.Callsign != "" || h.Tail != "N515DN" || h.Trigger != "RAT Shedding" {
		t.Errorf("a350 without a route = %+v", h)
	}

	// a later block of a long message has no frame header: it is a piece, not an undecodable message
	_, r = load(t, "real_miam_block")
	if r.Kind != KindMIAM || r.Tree != nil || r.Summary != SummaryMIAMBlock || r.Identity.Flight != "SQ23" {
		t.Errorf("block: kind %v tree %v summary %q flight %q", r.Kind, r.Tree != nil, r.Summary, r.Identity.Flight)
	}
}

// Delta's gate departure report, plain (label 4M) and inside a compressed MIAM message.
func TestDeltaDepartureReport(t *testing.T) {
	_, r := load(t, "real_delta_departure_plain")
	d, ok := r.Fields["departure"].(DepartureReport)
	if r.Kind != KindDeparture || !ok || d.Origin != "KPDX" || d.Dest != "KLAX" || d.Flight != "2459" {
		t.Fatalf("kind %v departure %+v", r.Kind, d)
	}
	if d.Heading["taxi"] != 289 || d.Heading["out"] != 35 || d.Events["taxi"]["MOVEMENT"] != "025133" || d.Events["out"]["OUT EVENT"] != "024658" {
		t.Errorf("departure = %+v", d)
	}
	if _, there := d.Events["out"]["GALLY CLSD"]; there {
		t.Errorf("an event that did not happen (------) is listed: %+v", d.Events["out"])
	}
	if want := "Departure report flight 2459 KPDX→KLAX: out 02:46:58Z, taxi 02:51:33Z"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}

	_, r = load(t, "real_delta_departure_miam")
	d, ok = r.Fields["departure"].(DepartureReport)
	if r.Kind != KindMIAM || !ok || d.Origin != "KLAX" || d.Dest != "VHHH" || d.Flight != "87" || d.Events["out"]["CARGO CLSD"] != "063720" {
		t.Fatalf("kind %v departure %+v", r.Kind, d)
	}
	if want := "Departure report flight 87 KLAX→VHHH: out 06:44:43Z, taxi 06:53:01Z"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}
}

// The UPS 747 position report sent at the top of descent: the usual *POS with a ground address in front and the route behind.
func TestUPSTopOfDescent(t *testing.T) {
	f, r := load(t, "real_ups_tod_hkg_anc")
	if r.Kind != KindPOS || r.Pos == nil || r.Fields["top_of_descent"] != true {
		t.Fatalf("kind %v pos %+v fields %v", r.Kind, r.Pos, r.Fields)
	}
	near(t, "lat", r.Pos.Lat, 60+44.0/60, 1e-6)
	near(t, "lon", r.Pos.Lon, -(152 + 41.0/60), 1e-6)
	if *r.Pos.AltFt != 20900 || r.Fields["mach"] != 0.664 || *r.Wx.TempC != -39 || *r.Wx.WindDirDeg != 154 || *r.Wx.WindKt != 17 {
		t.Errorf("alt %v fields %v wx %+v", *r.Pos.AltFt, r.Fields, r.Wx)
	}
	if r.Fields["origin"] != "VHHH" || r.Fields["dest"] != "PANC" {
		t.Errorf("route = %v → %v", r.Fields["origin"], r.Fields["dest"])
	}
	if want := time.Date(2026, 10, 31, 21, 24, 0, 0, time.UTC); !r.Pos.At(f.Received).Equal(want) {
		t.Errorf("time = %v, want %v", r.Pos.At(f.Received), want)
	}
	if want := "Position report 60.733N 152.683W FL209, M0.664, top of descent VHHH→PANC"; r.Summary != want {
		t.Errorf("summary = %q, want %q", r.Summary, want)
	}
	// the other airports are written the same way
	if _, r = load(t, "real_ups_tod_anc_ont"); r.Fields["origin"] != "PANC" || r.Fields["dest"] != "KONT" {
		t.Errorf("route = %v → %v", r.Fields["origin"], r.Fields["dest"])
	}
	// the ordinary report has no route and is no top of descent
	if _, r = load(t, "pos_ontime"); r.Fields["top_of_descent"] != nil || r.Fields["dest"] != nil {
		t.Errorf("ordinary report: %v", r.Fields)
	}
}

// Real predicted-winds messages: the table behind "- #M1" block headers (one cuts ONIKU in two), a request, a receipt, and the aircraft's own with descent winds.
func TestPWI(t *testing.T) {
	r, err := Decode(Message{Label: "H1", Dir: Uplink, Text: "- #M1PWI/WD400,ECOMI,278067,400M54.NATES,251091,400M53.NIKLL,241096,400M53.NYMPH,239099,400M53.NUZAN,227098,400M51.NRKEY,224091,400M49.NIPPI,224088,400M48.NOGAL,244079,400M46.NUBDA,271105,400M50.NANNO,274107,400M51.NODAN- #M1,281118,400M54.NANAC,286132,400M57.NIKON,287134,400M59.IXE,287120,400M58.AKAGI,286113,400M57.SUGAL,283113,400M57.ADGUN,281113,400M56.PIONE,280113,400M55.MARCO,277107,400M55.DGC,276103,400M55.ASOVO,276092,400M55.ONIK- #M1U,276083,400M55.SADLI,275077,400M55.DUMET,270070,400M56.N59000W170000,290036,400M46/WD380,ECOMI,284080,380M55.NATES,247098,380M55.NIKLL,238096,380M53.NYMPH,235098,380M53.NUZAN,225093,380M50.NRKEY,223089,380M48.NIPPI- #M1,224086,380M48.NOGAL,244078,380M45.NUBDA,276108,380M50.NANNO,280113,380M51.NODAN,286136,380M53.NANAC,287149,380M55.NIKON,286146,380M56.IXE,286130,380M56.AKAGI,285118,380M55.SUGAL,281108,380M54.ADGUN,280104,380M54.PI- #M1ONE,279107,380M52.MARCO,278106,380M52.DGC,276102,380M52.ASOVO,276092,380M51.ONIKU,275083,380M50.SADLI,275078,380M50.DUMET,271070,380M50.N59000W170000,295039,380M46/WD360,ECOMI,285105,360M55.NATES,238091,360M58.NIKLL- #M1,233086,360M53.NYMPH,230089,360M53.NUZAN,223085,360M49.NRKEY,223085,360M48.NIPPI,224083,360M48.NOGAL,244077,360M44.NUBDA,281113,360M50.NANNO,285121,360M51.NODAN,290157,360M51.NANAC,287158,360M52.NIKON,286147,360M52.- #M1IXE,285130,360M51.AKAGI,283115,360M51.SUGAL,277101,360M51.ADGUN,278093,360M50.PIONE,279097,360M49.MARCO,278102,360M48.DGC,277100,360M47.ASOVO,275090,360M46.ONIKU,273081,360M45.SADLI,273077,360M45.DUMET,270070,360M45- #M1.N59000W170000,297042,360M47/WD340,ECOMI,281128,340M53.NATES,229085,340M55.NIKLL,227072,340M54.NYMPH,224076,340M53.NUZAN,221075,340M48.NRKEY,224079,340M48.NIPPI,225078,340M49.NOGAL,242078,340M44.NUBDA,284119,340M49.- #M1NANNO,287129,340M49.NODAN,290166,340M48.NANAC,288158,340M47.NIKON,287143,340M47.IXE,285124,340M47.AKAGI,281111,340M46.SUGAL,274098,340M46.ADGUN,273087,340M46.PIONE,275088,340M45.MARCO,277091,340M44.DGC,278095,340M43- #M1.ASOVO,274086,340M42.ONIKU,270078,340M41.SADLI,268075,340M41.DUMET,267071,340M40.N59000W170000,296045,340M4967A5\n\t"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := r.Fields["pwi"].(PWI)
	if r.Kind != KindPWI || len(p.Levels) != 4 || p.Levels[0].FL != 400 || len(p.Levels[0].Points) != 25 {
		t.Fatalf("%v, %d levels", r.Kind, len(p.Levels))
	}
	first, last := p.Levels[0].Points[0], p.Levels[0].Points[24]
	if first.Fix != "ECOMI" || first.Dir != 278 || first.Speed != 67 || *first.TempC != -54 || last.Fix != "N59000W170000" || last.Speed != 36 || *last.TempC != -46 {
		t.Errorf("first %+v last %+v", first, last)
	}
	if got := p.Levels[0].Points[21].Fix; got != "ONIKU" {
		t.Errorf("fix across a block boundary = %q", got)
	}
	if want := "Predicted winds FL400/380/360/340, 25 fixes ECOMI → N59000W170000"; !strings.HasPrefix(r.Summary, want) {
		t.Errorf("summary = %q", r.Summary)
	}

	for _, c := range []struct{ label, text, want string }{
		{"H1", "F56AFJ0880#M1BREQPWI/TS141904,311026/WQ390.370.350.240:N10000W163300.15N60.RUDEE/DQ390/WRCYVR.KSEAE174", "Wind request FL390/370/350/240, 3 fixes N10000W163300 → RUDEE"},
		{"H1", "REQPWI/TS054236,301026/CQ340/DQ34026A6", "Wind request: climb FL340, descent FL340"},
		{"H1", "RESPWI/RJ,302A18B", "Wind data rejected"},
		{"H1", "F28AQF0018#M1BRESPWI/AK,71605F", "Wind data acknowledged"},
		{"H1", "PWI/WD390,JAGWA,275040.BURGL,288047.DOUIT,280029/DD300274028.200234019.100213006/SN6702F0\n\n", "Predicted winds FL390, 3 fixes JAGWA → DOUIT, descent winds"},
	} {
		r, _ := Decode(Message{Label: c.label, Text: c.text})
		if r.Kind != KindPWI || r.Summary != c.want {
			t.Errorf("%q: %v %q, want %q", c.text, r.Kind, r.Summary, c.want)
		}
	}
	r, _ = Decode(Message{Label: "H1", Text: "PWI/WD390,JAGWA,275040.BURGL,288047.DOUIT,280029/DD300274028.200234019.100213006/SN6702F0"})
	if d := r.Fields["pwi"].(PWI).Descent; len(d) != 3 || d[1] != (LevelWind{FL: 200, Dir: 234, Speed: 19}) {
		t.Errorf("descent winds %+v", d)
	}
	if _, ok := ParsePWI("PWI/ nothing here"); ok {
		t.Error("an empty PWI decoded")
	}
}

// Real airline operations messages: Qantas movements, flight data link and weather requests, Air Canada's movement,
// event, flight information, load sheet and dangerous goods messages, pre-departure clearances, the ATIS and METAR
// sent to the aircraft, and data link disconnects. What each says is its summary.
func TestOpsMessages(t *testing.T) {
	for _, c := range []struct{ label, text, want string }{
		{"81", "\r\nMVA\r\nQFA0008/24.VHOQG.YSSY\r\nAA2008/2012", "Qantas QF8 arrival at YSSY: on 20:08Z, in 20:12Z"},
		{"80", "\r\nMVA\r\nQFA0007/25.VHOQG.YSSY\r\nAD0213/0233 EA1725 KDFW", "Qantas QF7 departure YSSY→KDFW: out 02:13Z, off 02:33Z, ETA 17:25Z"},
		{"80", "\r\nMVA\r\nQFA0008/  .VHOQG.    \r\nAD2022                 \r\nSI DR2022", "Qantas QF8 departure: out 20:22Z"},
		{"RF", ",FDASUB01261101034409,SYD,,SIN,,QF001,261101,0530,388,,VH-OQG,/351247,361,,1,AC2A", "Qantas flight data submitted: QF1 YSSY→WSSS, scheduled 05:30Z"},
		{"RF", ",FDAACK01261101034415/351247,361,,1,47D1", "Qantas flight data acknowledged"},
		{"5U", "\r\nWX REQUEST\r\nQFA0001/27\r\nACTUAL\r\nEGLL EHAM LFPG", "Weather request (METAR) for EGLL, EHAM, LFPG"},
		{"5U", "M75AFJ0880  01 WXRQ   0880/31 NFFN/CYVR .DQ-FAM\n\t/TYP 1/STA KSFO/STA KPDX/STA CYVR", "Weather request NFFN→CYVR for KSFO, KPDX, CYVR"},
		{"5U", "U06AQF0018\n\tWX REQUEST\n\tQFA0018/25\n\tATIS   \n\tYSSY YMML", "D-ATIS request for YSSY, YMML"},
		{"4T", "ATD AC0025/31/31 YVR 1921Z", "Air Canada AC25 left the gate at CYVR: out 19:21Z"},
		{"4T", "AGFSR AC0025/31/31/YVRPVG/2218Z/833/5847.5N15356.8W/340/CRUISE/0593/0199/M41/214017/0295/***/493/1921/1940/****/****", "Air Canada AC25 position report CYVR→ZSPD: 58.792N 153.947W, FL340, cruise"},
		{"4Q", "TDX AC0025/31/31 YVR 1921Z 1940Z 833  \r0791/089528 L A", "Air Canada AC25 left the gate and took off at CYVR: out 19:21Z, off 19:40Z"},
		{"4Q", "KQ1 AC0020/31/31 SIN YVR/0037/0057/1427/1434", "Air Canada AC20 movements WSSS→CYVR: out 00:37Z, off 00:57Z, on 14:27Z, in 14:34Z"},
		{"49", "01ICCL    ACA025/311941CYVRZSPD\r\n+ 1888221.4+ 13.0", "Air Canada AC25 event ICCL CYVR→ZSPD at 19:41Z"},
		{"49", "01DCAPUAL27     /260855KDENEGLL\r\n+ 1258150.5+ 12.0", "United UA27 event DCAP KDEN→EGLL at 08:55Z"},
		{"17", "01TOICCKK206    /301712EHAMZSPD\r\n201   196.7 +15.1", "China Cargo CK206 event TOIC EHAM→ZSPD at 17:12Z"},
		{"4C", "-FLT CA01 0025/31/YVR/PVG/CYVR/ZSPD/ACA025\r\n\r\n", "Air Canada flight information AC25 CYVR→ZSPD"},
		{"4M", "-// LDF MA01 S4APRD1 31OCT/1914 YVR  ACK C-FNOH/833/0025 24721685000\r\n 0025/31/YVR/PVG/01\r\n\r\nLOAD FINAL       AC0025  31OCT   YVR/PVG\r\nFIN833 CFNOH     OFP-1   LS-1     EDNO-1\r\n\r\nZFW    168.5                 CRW PAX INF\r\nTOW    247.2                 3/9 294   2\r\nTOWCG   26.0                 \r\n\r\nFOB*    79.2                  0A  0B  0C\r\nUNU      0.0                  30 131 133\r\nTOF     78.7\r\n\r\n                             FWD AFT BLK\r\nLM      17.1        LIVE     [ ] [ ] [ ]\r\nCT      45.0        PRSHBLE  [Y] [Y] [ ]\r\nRM      17.1        DG       [Y] [ ] [ ]\r\n            \r\n            \r\nRMKS   [EMS/23P RRY/23P                ]\r\n       [                               ]\r\n       [                               ]\r\n       [                               ]\r\n       [                               ]\r\n       [                               ]\r\n       [                               ]\r\n                                        \r\n        EMER RESPONSE GUIDE DRILL CODES \r\nFWD    [9L, 7L, 2L                     ]\r\nAFT    [                               ]\r\nBLK    [                               ]\r\n\r\nVIC RAVANESE             +1-905-861-7403\r\n-- PART 1 OF 4 --\r\n\r\n", "Air Canada load sheet (final) AC25 CYVR→ZSPD: zero fuel 168.5 t, take-off 247.2 t, fuel 79.2 t, 294 passengers"},
		{"4P", "-// PNT PA04 S4APRD1 31OCT/1914 YVR      C-FNOH/833/0025 24721685000\r\n\r\nLOAD FINAL NOTOC AC0025  31OCT  YVR/PVG   FIN833 CFNOH  OFP-1   LS-1  EDNO-1\r\n\r\n        SPECIAL LOAD - NOTIFICATION TO CAPTAIN\r\n        ======================================\r\nTHIS IS TO CONFIRM THAT THERE IS NO EVIDENCE OF DAMAGED OR LEAKING PACKAGES AND\r\nTHAT ULDS CONTAINING DANGEROUS GOODS LOADED ON THE AIRCRAFT ARE FREE FROM ANY\r\nEVIDENCE OF LEAKAGE OR DAMAGE. ALL BULK LOADED DANGEROUS GOODS PACKAGES HAVE\r\nBEEN SECURED AGAINST MOVEMENT IN FLIGHT.\r\n\r\nLA PRESENTE CONFIRME QUE, SELON TOUTE APPARENCE, AUCUNE EXPEDITION (COLIS OU\r\nUC) DE MARCHANDISES DANGEREUSES ENDOMMAGEES OU COMPORTANT UNE FUITE N'A ETE\r\nCHARGEE A BORD DE L'APPAREIL. TOUTES LES EXPEDITIONS DE MARCHANDISES DANGEREUSES\r\nCHARGEES EN SOUTE ONT ETE ARRIMEES POUR EVITER LEUR DEPLACEMENT DURANT LE VOL.\r\n\r\nVERIFIED/VERIFIE BY/PAR: AC075707\r\n-- PART 4 OF 4 --\r\n\r\n", "Air Canada dangerous goods notice (NOTOC) AC25 CYVR→ZSPD"},
		{"A3", "/PVGDCYA.DC1/CLD 1245 261029 ZSPD\n\tPDC 999\n\tUPS81 CLRD TO PANC OFF 34L VIA LAMEN2\n\tSQUAWK 6331 ADT 1245 NEXT FREQ 121.950 ATIS Y\n\tINITIAL ALT 900M\n\tFL 7500M\n\tWHEN AIRBORNE CTC APP125.40 W/O INSTRUC78B0", "Departure clearance UPS81 ZSPD→PANC: runway 34L, LAMEN2, squawk 6331"},
		{"A4", "/PVGDCYA.FS1/FSM 1245 261029 ZSPD\n\tUPS81 RCD RECEIVED\n\tREQUEST BEING PROCESSED\n\tSTANDBY29F9", "ZSPD clearance service: UPS81 request received, standby"},
		{"A4", "/MADAAYA.FS1/FSM 0501 261024 LEZG\n\tAN B-LRM RAI REJECTED\n\tERROR IN MESSAGE\n\tRESUBMIT YOUR REQUEST\n\tUNKNOWN AIRPORT CODEA062", "LEZG clearance service: B-LRM request rejected (unknown airport code)"},
		{"3T", "CYXS  2SA   METAR CYXS 010200Z AUTO\n\t   16007KT 9SM CLR     \n\t   M01/M03 A2996 RMK   \n\t   SLP186= \n\tMETAR CYXS 010100Z AUTO\n\t   17006KT 9SM CLR     \n\t   M00/M03 A2996 RMK   \n\t   SLP186=", "METAR CYXS 02:00Z: 160°/7 kt, 9 SM, CLR, -1/-3 °C, QNH 29.96"},
		{"A9", "/DATSCXS.TI2/HNL  ARR ATIS P\n\t1453Z -  HNL ATIS INFO P\n\t1453Z. 06008KT 10SM\n\tFEW022 BKN030 OVC060\n\t26/22 A2988 (TWO NINER\n\tEIGHT EIGHT) RMK AO2\n\tSLP118 T02560222 53000.\n\tLNDG/DEPG RWYS 4/8 EXP\n\tRNAV OR VISUAL APCH. RWY\n\t4R CLSD. TWY, E BTN TWY,\n\tA AND TWY B CLSD, TWY, H\n\tBTN RWY, 8L AND TWY A\n\tCLSD, TWY, C BTN RWY, 4R\n\tAND TWY RT CLSD, TWY, K\n\tBTN RWY, 4L AND RWY 4R\n\tCLSD, TWY A BTN RWY 4L\n\tAND TWY K CLSD. TWY W\n\tCLSD. SOUTH TEE HANGAR\n\tENTRANCE CLSD. TWY C BTN\n\tRWY 4L AND TAXI LANE 1\n\tCLSD. TWY T CLSD TWY P\n\tCLSD. RWY 8R DEP END\n\tBARRIER OTS. ILS RWY  8L\n\tOTS, EWABE OM OTS. HNL\n\tTACAN AZM OTS. CKH TACAN\n\tAZM OTS. READ BACK ALL\n\tCONTROL INSTRUCTIONS.\n\tINCR BIRD STRIKE HAZ ALL\n\tRWYS. NUMEROUS CRANES IN\n\tTHE VCNTY HNL ARPT CHECK\n\tNOTAMS FOR ADNL INFO.\n\tAIRMET S UPDATE 1 VALID.\n\tMORE INFO AT HNL AFSS.\n\tWARNING RWY 8L IS\n\tSHORTENED 7000 FT\n\tAVAILABLE LAST EXIT TWY\n\tY, S, D INTERSECTION. GC\n\tAND CD FREQUENCY 118.1.\n\t...ADVS YOU HAVE INFO P.2961", "D-ATIS arrival information P for PHNL: 060°/8 kt, 10 SM, FEW022 BKN030 OVC060, 26/22 °C, QNH 29.88"},
		{"B6", "J70ACX0749/CGKCDYA.DIS..B-LRD20CDE6", "Data link disconnect: B-LRD"},
		{"B6", "J20ADL0038/ANCATYA.DIS.N501DN800610", "Data link disconnect: N501DN"},
	} {
		r, err := Decode(Message{Label: c.label, Text: c.text})
		if err != nil || r.Summary != c.want {
			t.Errorf("%s %.50q: %v %q, want %q", c.label, c.text, r.Kind, r.Summary, c.want)
		}
	}

	op := func(label, text string) (Result, OpsReport) {
		r, _ := Decode(Message{Label: label, Text: text})
		o, _ := r.Fields["ops"].(OpsReport)
		return r, o
	}
	// the times and route that the flight's route and departure come from
	r, o := op("80", "\r\nMVA\r\nQFA0007/25.VHOQG.YSSY\r\nAD0213/0233 EA1725 KDFW")
	if r.Kind != KindOps || o.Origin != "YSSY" || o.Dest != "KDFW" || o.Out != "0213" || o.Off != "0233" || o.ETA != "1725" || r.Identity.Flight != "QF7" || r.Dir != Downlink {
		t.Errorf("Qantas departure: %+v %+v", o, r.Identity)
	}
	r, o = op("4Q", "KQ1 AC0020/31/31 SIN YVR/0037/0057/1427/1434")
	if o.Origin != "WSSS" || o.Dest != "CYVR" || o.Out != "0037" || o.Off != "0057" || o.On != "1427" || o.In != "1434" {
		t.Errorf("Air Canada movements: %+v", o)
	}
	// asterisks stand for a time not yet known
	if _, o = op("4T", "PSHBK AC0025/31/31 YVR 1921Z/****Z/****Z/1922Z"); o.Out != "1921" || o.Off != "" {
		t.Errorf("pushback: %+v", o)
	}
	// the position report is a position
	r, _ = op("4T", "AGFSR AC0025/31/31/YVRPVG/2218Z/833/5847.5N15356.8W/340/CRUISE/0593/0199/M41/214017/0295/***/493/1921/1940/****/****")
	if r.Pos == nil || r.Pos.Basis != BasisDay || r.Pos.Secs != 22*3600+18*60 || *r.Pos.AltFt != 34000 || r.Wx == nil || *r.Wx.TempC != -41 || *r.Wx.WindDirDeg != 214 || *r.Wx.WindKt != 17 {
		t.Errorf("position: %+v %+v", r.Pos, r.Wx)
	}
	near(t, "lat", r.Pos.Lat, 58+47.5/60, 1e-9)
	near(t, "lon", r.Pos.Lon, -(153 + 56.8/60), 1e-9)
	// a clearance names the runway, SID and squawk
	if _, o = op("A3", "/PVGDCYA.DC1/CLD 1245 261029 ZSPD\n\tPDC 999\n\tUPS81 CLRD TO PANC OFF 34L VIA LAMEN2\n\tSQUAWK 6331 ADT 1245 NEXT FREQ 121.950 ATIS Y\n\tINITIAL ALT 900M\n\tFL 7500M\n\tWHEN AIRBORNE CTC APP125.40 W/O INSTRUC78B0"); o.Origin != "ZSPD" || o.Dest != "PANC" || o.Runway != "34L" || o.SID != "LAMEN2" || o.Squawk != "6331" {
		t.Errorf("clearance: %+v", o)
	}
	// the ATIS has the weather in it
	r, _ = Decode(Message{Label: "A9", Text: "/DATSCXS.TI2/HNL  ARR ATIS P\n\t1453Z -  HNL ATIS INFO P\n\t1453Z. 06008KT 10SM\n\tFEW022 BKN030 OVC060\n\t26/22 A2988 (TWO NINER\n\tEIGHT EIGHT) RMK AO2\n\tSLP118 T02560222 53000.\n\tLNDG/DEPG RWYS 4/8 EXP\n\tRNAV OR VISUAL APCH. RWY\n\t4R CLSD. TWY, E BTN TWY,\n\tA AND TWY B CLSD, TWY, H\n\tBTN RWY, 8L AND TWY A\n\tCLSD, TWY, C BTN RWY, 4R\n\tAND TWY RT CLSD, TWY, K\n\tBTN RWY, 4L AND RWY 4R\n\tCLSD, TWY A BTN RWY 4L\n\tAND TWY K CLSD. TWY W\n\tCLSD. SOUTH TEE HANGAR\n\tENTRANCE CLSD. TWY C BTN\n\tRWY 4L AND TAXI LANE 1\n\tCLSD. TWY T CLSD TWY P\n\tCLSD. RWY 8R DEP END\n\tBARRIER OTS. ILS RWY  8L\n\tOTS, EWABE OM OTS. HNL\n\tTACAN AZM OTS. CKH TACAN\n\tAZM OTS. READ BACK ALL\n\tCONTROL INSTRUCTIONS.\n\tINCR BIRD STRIKE HAZ ALL\n\tRWYS. NUMEROUS CRANES IN\n\tTHE VCNTY HNL ARPT CHECK\n\tNOTAMS FOR ADNL INFO.\n\tAIRMET S UPDATE 1 VALID.\n\tMORE INFO AT HNL AFSS.\n\tWARNING RWY 8L IS\n\tSHORTENED 7000 FT\n\tAVAILABLE LAST EXIT TWY\n\tY, S, D INTERSECTION. GC\n\tAND CD FREQUENCY 118.1.\n\t...ADVS YOU HAVE INFO P.2961"})
	w, _ := r.Fields["weather"].(WeatherReport)
	if r.Kind != KindWeather || w.Airport != "PHNL" || w.Info != "P" || w.Time != "1453" || *w.WindDir != 60 || *w.WindKt != 8 || *w.TempC != 26 || *w.DewC != 22 || w.QNH != "29.88" || w.Type != "arrival" {
		t.Errorf("ATIS: %v %+v", r.Kind, w)
	}
	// a request is still a request, a message of another label is left alone, and the Fiji POSRPT is not mistaken for ops
	if r, _ = Decode(Message{Label: "B9", Text: "/CYVR.TI2/030CYVRAA396"}); r.Kind != KindDATIS || r.Summary != "D-ATIS arrival request for CYVR" {
		t.Errorf("request: %v %q", r.Kind, r.Summary)
	}
	if r, _ = Decode(Message{Label: "4T", Text: "hello"}); r.Kind != KindUnknown {
		t.Errorf("unrecognised 4T: %v", r.Kind)
	}
}

// Engine and performance reports from the 787s (United, Air Canada): maintenance data that begins with the flight, the route
// and the full date and time, and usually the position. Real messages; one is the engine that had no position fix.
func TestEngineReports(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"APM    1 N24993         UAL182  KDENEDDF211026232013,\r\n,70000021,.843,,,274.5,,,-13.07,,,37001,,,485547,,,6483,6485,,,\r\n 92.26, 92.25,,,  1.069,545.7,,,\r\n 61.5, 60.9, 81.5, 80.9,,,0,0,1,,,,,, 43.7566,,,\r\n -92.7391,,,  71.0,,,  4.7,,,  75.4,,,  -0.001, 0.006,    0,,,\r\n 6.673,  15, 55543,36600,36900,,,,,,10553,10584,,,  4.3,\r\n 44, 41, 37, 42,505800,,, 0.0),,,355800,,, 76300,36700,37000,\r\n,,,,,958959,958974,,,,23.30,,,,,,105.8,106.0,,,\r\n,,,,803.1,811.2,,,,,,,-45.3,,, -2.2,,, -0.1,,,  2.7,\r\n,,  4.5, 6.4,,-11.2,-11.5,,, -0.7,,  7.6,  7.6,,, 0.0, 0.0,,,\r\n 0.00,,, 0.00,,, 0.00,,,,,  0.1\r\n", "United UA182 performance report KDEN→EDDF at 23:20Z: 43.757N 92.739W, FL370"},
		{"<701>IEG\r\n N24993UAL182    ER  72211026232013KDENEDDF4852219 C 40\r\n 370052742842-128 437566 -927391  709 1 5061\r\n  710  754  475457    0ENGD269NOTA 1069\r\n 37001-13072745843 3441-45284945 -01  27\r\nBSM5870000021 641\r\n958959  92261058 8031 64835990\r\n958974  92251060 8112 64855990\r\n 44 41 36700 76300 37000  15 6673 10553 10584\r\n 37 42 36600 55543 36900\r\n  43CLS)505800355800 00)4855472330 000 000 000\r\n 615 609 815 809\r\n -112  -15  0   76 -224 450  -07 000 008\r\n -115  -18  0   76 -237\r\n  00  00  01  00  00  00  01  -0001  0006\r\n  00  00  01  00  00  00  00", "United UA182 performance report KDEN→EDDF at 23:20Z: 43.757N 92.739W, FL370"},
		{"<701>IEG\r\n C-FNOHACA026    ER 449261026124551ZSPDCYVR4720879 C 31\r\n 370012729839-154 413159 1492495  294 311389\r\n  295  450 1585872    2ENGD309NOTA 3760\r\n 37000-15072728838 3425-46734902 -01  26\r\nBSM5A70990054 611\r\n956840  92171059 8005 64176160\r\n958063  92171058 7952 64196160\r\n 45 43 36817 65256 37037  12 6751 22266 22305\r\n 46 44 36817 21510 37038\r\n  20CLS)51911238000027004724062700 000 000 000\r\n 652 476 852 639\r\n -117  -18  0   56 -244 450  -05 000 006\r\n -117  -18  0   57 -242\r\n  00  00  01  01  01  00  00  -0009  0011\r\n  00  00  00  01  01  00  00", "Air Canada AC26 performance report ZSPD→CYVR at 12:45Z: 41.316N 149.250E, FL370"},
		{"/ILNGE7X.CLB006UA N249932UAL178    261024125111LIRFKDEN-074060295897486000013000022447M85P04B205B20504060F092122M32P08      2462M81P05  2462M82P05  -15510294+04160933+01203635007NQVK1UNANS01SVRVL8:::0FF0HGG00000481L410BHN00O00G01V29600C00A00K0000017V10Q10Q00009JA9ME9Q19ND9JR9JV9I59KA9J71NI01K02K0V70S76EE6FE6FH0PU      02OE0VA0BT31OU1GOB0V60F40EB111P03C0VP98D97M0AIU0SK1RG1FI1HT214N103FV1JB0RA6G91KT6QB0OC0RU10R401J1JF1I40V901G1521B61561BD0V70RS0010FQM0FQI0FKF::::::::::::::::::::::)))171G10I1L0G0K180R0597I9OG0TA0TA12312B0TS0UD1TJ0EB0RU0UK03E01I0000050002500O002400O:00000190002000:::00000060000001742002081000GP9V9J6000100000002030002601I9OJ9O29H26SK1JG0OA70P00L1Q0000007NQVK1UNANS01SVRVL8:::0FF0HGG00000481KB10LFR00H00D01D01100C00B00C000002AQ10Q10Q00009GF9JB9M99J99FF9ES9CV9HB9F91NJ01L02P0V70R86EE6ET6EU0PU      04HA0V90B731OA1GM00V30F30NA10SK0370L39B99AV0AFP0SL1PE1851AED0UH103MD1O70PS6DD1K56O00HE0Q310R401J1KF1I30V901G1601C616D1CK0V70RV0020DAB0DA90D8O::::::::::::::::::::::)))191K109130G0J0S0I049AP9OK0TV0TV12212B0UG0U81J50NB0Q30NL02E01I0000050002500G002400G000002000000190002000:::00000060000001742002081000GLPMJH600000D0301060300015U1J9KP9KH9ML6Q61II0OA70P00L1Q00000SS0T10TA0TL0TR0TM0TU0U30U60VJ//0TE0TR0U50UH0UR0UG0UU0VD10411P//01201301C01E01H02302702B02H02F//0TF0S20S40RU0RD0S10R00PD0MK0JV//0SJ0S10S10S70S70RT0RU0RT0RE0S4//J4J3J8JBJBJBJ7J3IKIN((01102N05906P08M0E20I00LG0RH10V//01TN", "United UA178 climb report LIRF→KDEN at 12:51Z: 41.609N 12.036E"},
		{"/ILNGE7X.CLB006UA N249932UAL27     261026010104KDENEGLL-072060295897486000013000022447M85P04B205B205040601092122M32P08      2462M81P05  2462M82P05  -00331855+00000000+00000000007NQVK1UNANS01SVRVL8:::0FF0HGG00000481O810BHE01201301301H00K00K00B0000007S10P10Q00008JU8KE8QP8Q68NV8NB8IJ8JT8KA1R801M02L0V70PS6FA6GG6GN0P00NE0MQ031B0V80C4320G1GMS0VC0DA0E510IB03E0VQ7UJ7V50A4K0R21Q61A9130A14K103061OB0TC6CP1GK63G0Q90TR10R401J1FC1EQ0V009114P19C14S19E0VA0P30000AM80AMI0A7R::::::::::::::::::::::)))131620A0Q16151S1Q0481O9QV0AQ0AR12312B0PG0TM1EH0E60TR10H03D01I0000050004500O004400O:00000190002000:::000000600000017426G2081000D3FT9IS010102000002020103551Q9S99RN9H96731FH0OA70P00L1Q0001007NQVK1UNANS01SVRVL8:::0FF0HGG00000481PQ10LGT00P00O0190BD00E00D00J0000029E10Q10Q00009DF9GH9IV9G19CG9C79B09E49CC1T201L02N0V70QI6F96FO6G00P00NE0N404D70V50CD31O31GMN0V60F00KG10PU03D0L297D9750AE70SH1QA14K164I0S4103FI1O70PP6CU1JH6LQ0J90Q410R401J1IN1GS0V801G15I1BI1621C10V80RQ0020BSF0BSJ0BQ8::::::::::::::::::::::)))161H10F150M0Q1917049739OF0UI0UI12212B0U40U71EC0KG0Q40PG02B01I0000050004500G004400G000002000000190002000:::00000060000001742002081000D16AJI5000002000004020002611I9RE9R0A116O21HO0OA70P00L1Q0001//0S40SC0SR0T00T90VG10110910B///0U70UH0UT0VF0VF10E11811N12D///01B01E01G01H01V02102602G02L///0TR0TI0S80QE0RA0P40N50M70J5///0RJ0RL0RM0RA0RH0T20TA0T80SI/((IOI7IVINIUJFJMJBIV(//00102803K05S0A70D00G810S16M/06TN", "United UA27 climb report KDEN→EGLL at 01:01Z"},
		{"/ILNGE7X.SCR005UA N249932UAL27     261026030724KDENEGLL-072070295897486000013000022447M85P04B205B205040609092122M32P08      2462M81P05  2462M82P05  +06842285+04986141-08165046007NQVK1UNANS01SURVL8:::0FF0HGG00000481M81181900G00D01503O00R00R00H0000028501H01I00008368588EL89C8247UL7Q586P8251NG01M02L0V70L56F76HI6HN0O20L80KT05OU0V40J031MV1GR30VA0R90Q410A403E08T8KJ8KP0A3K0RU1P10I30IET0E3103LC1O60H65R118O5QT0880HR10R401J1HK1E90V801G18V1EQ18T1F50V70PD002057N057O056P::::::::::::::::::::::)))1H1A20E0V1C1D0R0K048KN9OH08008012212B0TM0US0MT0Q60HR0EG01401I0000050004500O004400O000102000000190002000:::00000060000001743PG2081000CA0E   0202000200040500025J1I00009S05T516G00070P00L1Q0001", "United UA27 cruise report KDEN→EGLL at 03:07Z: 49.861N 81.650W"},
		{"<301>JEG9   1\r\n N24993UAL27     TA  79261026005050KDENEGLL  279\r\n 39857-1046752692690BSM5870000021\r\nTA0539 1 1 020 0 7 0112343\r\nER 1 1 0 0 0 8 01\r\nTA0539 1.. 012 0 6 0112340\r\nER 1.. 0 0 0 8 01\r\nTA  5096  12410) 3) 840  124  512200\r\nER 36997 -262836272 217 -563  605200\r\nTA 90 3128368    010107 63 33 33\r\nER 69 70352363513222107107 33 33\r\nTA 26 24205392054711107107 84 84\r\nER 68 66347983468922107107 84 84\r\nTA 98  2  2 98  0  0100  0\r\nER  2  2 10 10  0  0 12 14\r\nTA  0  2  3 54  0 24100100\r\nER 13 14 21 21  3  3 17 28\r\nTA 129   0 193  97120120166120\r\nER  59  59 256 252 47 48162163\r\nTA  60  62 156 113121122152151\r\nER  48  54 250 259 46 48154155\r\nTA128 2084 102  52  39  58 36 62 30 98  0\r\nER110 1851  81  -1  35  49 43 98 98 59 98\r\nTA 92 1502  97  52  43  57 28 73  2 98  0\r\nER102 1716  82 -12  30  49 34 98 98 62 98\r\nTA  2  2 981032311155   0  0 564  0 79 52  0\r\nER 70 90  2 6138 9054   0 23 564205 72 52  0\r\nTA  2  2 5010308 9295   0  0 571  0 78 52  0\r\nER 70 90  2 6141 7813   0 24 562204 79 62  0\r\n  73  74  81 26  73  74 101 30\r\n  66  2  99 27\r\n  72  72  5101  72  72  7901\r\n  71  72  60  2  72  72  68 33\r\n  72  72  6401  70  72  5501\r\n  73  72  66 32  74  72  51  2\r\n  71  72  60  2  73  72  57 22\r\n  60  57\r\n  74  72  60 26  72  72  52  2\r\n  60  52\r\nTA22  9  72222\r\nER22  9  72222\r\nTA221     0\r\nER221     0", "United UA27 performance report KDEN→EGLL at 00:50Z: 39.857N 104.675W"},
		{"<310>JEG\r\n9  N24993UAL27     KDENEGLL261026005743  79TO431192  1\r\n  50931035173  114 39841-104693 -887BSM5870000021  692\r\nUS    \r\n 10025  144 6961191010112\r\n    0  100  365  320 0 0\r\n    0    0  345  285 0 0\r\n  5703  5703  14  29\r\n  5702  5703  14  29\r\n  915  782  302  306\r\n -1270040FFFAAD66E8A01B0000C06C00C0420601140CFB\r\n -12700007FF29166AFA0080000C05E0040020401140C41\r\n -12700007FF6EA66C0A00A0000C02D0040020401140C41\r\n   295440FFFE0C66A5A01B0000C0F320C0420701140CC4\r\n0074000001140C51000000000002000000\r\n0050000001140C64000000000002000000\r\n0050000001140C64000000000002000000\r\n0074000001140C51000000000002000000\r\n   117  24 304\r\n 36997 -262 2171175020606\r\n    0  100  335  145 0 0\r\n  100    0  315  110 0 0\r\n  6051  6052   8  15\r\n  6052  6053   8  15\r\n  966  899  215  213\r\n -1270040FFFA866592A01B0000C01500C0430601140C39\r\n -12700007FF2EC654EA0080000C0C40040030401140C83\r\n -12700007FF6E56576A00A0000C0100040030401140C83\r\n   159140FFFE176557A01B0000C0F220C0430701140C06\r\n0074000001140C51000000000002000000\r\n0050000001140C64000000000002000000\r\n0050000001140C64000000000002000000\r\n0074000001140C51000000000002000000\r\n     7  94 304\r\n 41000 -247 1791175081535\r\n    0    0  300   70 0 0\r\n    0    0  280   40 0 0\r\n  6050  6051   8  15\r\n  6051  6050   8  15\r\n  969  904  209  211\r\n -1270040FFFE9F658DA0180000C05300C0430601140C39\r\n -12700007FF6C66551A0090000C0700040030401140C83\r\n -12700007FF2EB656FA0090000C07D0040030401140C83\r\n   152940FFFA69654FA0190000C0AC20C0430701140C06\r\n0074000001140C51000000000002000000\r\n0050000001140C64000000000002000000\r\n0050000001140C64000000000002000000\r\n0074000001140C51000000000002000000\r\n    18  94 304", "United UA27 take-off report KDEN→EGLL at 00:57Z: 39.841N 104.693W"},
		{"/ILNGE7X.TOF005AC C-FNOH2ACA23     261026220430CYVRRJBB-004050295806374000013000022447M85P04B205B205040600092122M32P08      2462M81P05  2462M82P05  -08129681+04918982-12320592057NQVK1UNANSH1SVRVT8:::0FF0HGG00000481KQ1027700U00K02618S00H00800Q0LN0005P10V10Q00069L99K69SA9TP9RO9Q99MR9NO9N21NU01A0310V70TP0PE0PH0PI0NO      01OF0V304P31TJ1FPQ0V70G808O11AP0391DF93G9300AJR0RI1RS1TK1T9A1G0103N91O00UN6IR1LU6MI0SC0TO10QU01O1BL1K10V901G13B19D13019T0V70RF0000K4A0K4D0JLQ::::::::::::::::::::::)))2B0N10Q1P0E0U1G120692U9OF0SF0SF12512B0T30UB2CP08P0TM12I04R01I0000050002500O004400O000008000000190002000:::00000060000001742002081000FK6509T010102000001070007661I9GT9270HD10GU00027T1103T1LC6P81KL2VBR979AA        0OA70P00L1Q0000", "Air Canada AC23 take-off report CYVR→RJBB at 22:04Z: 49.190N 123.206W"},
	} {
		r, err := Decode(Message{Label: "H1", Text: c.text})
		if err != nil || r.Kind != KindOps || r.Summary != c.want {
			t.Errorf("%.40q: %v %q, want %q", c.text, r.Kind, r.Summary, c.want)
		}
	}

	dec := func(text string) Result {
		r, _ := Decode(Message{Label: "H1", Text: text})
		return r
	}
	// the whole date is in the message, so a report delivered two days late is placed when it happened
	recv := time.Date(2026, 10, 23, 21, 24, 0, 0, time.UTC)
	r := dec("<701>IEG\r\n N24993UAL182    ER  72211026232013KDENEDDF4852219 C 40\r\n 370052742842-128 437566 -927391  709 1 5061\r\n  710  754  475457    0ENGD269NOTA 1069\r\n 37001-13072745843 3441-45284945 -01  27\r\nBSM5870000021 641\r\n958959  92261058 8031 64835990\r\n958974  92251060 8112 64855990\r\n 44 41 36700 76300 37000  15 6673 10553 10584\r\n 37 42 36600 55543 36900\r\n  43CLS)505800355800 00)4855472330 000 000 000\r\n 615 609 815 809\r\n -112  -15  0   76 -224 450  -07 000 008\r\n -115  -18  0   76 -237\r\n  00  00  01  00  00  00  01  -0001  0006\r\n  00  00  01  00  00  00  00")
	if r.Pos == nil || !r.Pos.At(recv).Equal(time.Date(2026, 10, 21, 23, 20, 0, 0, time.UTC)) || r.Identity.Tail != "N24993" || r.Identity.Flight != "UA182" {
		t.Errorf("late report: %+v %+v", r.Pos, r.Identity)
	}
	near(t, "lat", r.Pos.Lat, 43.7566, 1e-9)
	near(t, "lon", r.Pos.Lon, -92.7391, 1e-9)
	if *r.Pos.AltFt != 37005 {
		t.Errorf("altitude %v", *r.Pos.AltFt)
	}
	// a take-off report gives the moment of take-off, to the second, and no hhmm to be placed on a guessed day
	o := dec("<310>JEG\r\n9  N24993UAL27     KDENEGLL261026005743  79TO431192  1\r\n  50931035173  114 39841-104693 -887BSM5870000021  692\r\nUS    \r\n 10025  144 6961191010112\r\n    0  100  365  320 0 0\r\n    0    0  345  285 0 0\r\n  5703  5703  14  29\r\n  5702  5703  14  29\r\n  915  782  302  306\r\n -1270040FFFAAD66E8A01B0000C06C00C0420601140CFB\r\n -12700007FF29166AFA0080000C05E0040020401140C41\r\n -12700007FF6EA66C0A00A0000C02D0040020401140C41\r\n   295440FFFE0C66A5A01B0000C0F320C0420701140CC4\r\n0074000001140C51000000000002000000\r\n0050000001140C64000000000002000000\r\n0050000001140C64000000000002000000\r\n0074000001140C51000000000002000000\r\n   117  24 304\r\n 36997 -262 2171175020606\r\n    0  100  335  145 0 0\r\n  100    0  315  110 0 0\r\n  6051  6052   8  15\r\n  6052  6053   8  15\r\n  966  899  215  213\r\n -1270040FFFA866592A01B0000C01500C0430601140C39\r\n -12700007FF2EC654EA0080000C0C40040030401140C83\r\n -12700007FF6E56576A00A0000C0100040030401140C83\r\n   159140FFFE176557A01B0000C0F220C0430701140C06\r\n0074000001140C51000000000002000000\r\n0050000001140C64000000000002000000\r\n0050000001140C64000000000002000000\r\n0074000001140C51000000000002000000\r\n     7  94 304\r\n 41000 -247 1791175081535\r\n    0    0  300   70 0 0\r\n    0    0  280   40 0 0\r\n  6050  6051   8  15\r\n  6051  6050   8  15\r\n  969  904  209  211\r\n -1270040FFFE9F658DA0180000C05300C0430601140C39\r\n -12700007FF6C66551A0090000C0700040030401140C83\r\n -12700007FF2EB656FA0090000C07D0040030401140C83\r\n   152940FFFA69654FA0190000C0AC20C0430701140C06\r\n0074000001140C51000000000002000000\r\n0050000001140C64000000000002000000\r\n0050000001140C64000000000002000000\r\n0074000001140C51000000000002000000\r\n    18  94 304").Fields["ops"].(OpsReport)
	if o.OffAt != "2026-10-26T00:57:43Z" || o.Off != "" || o.Origin != "KDEN" || o.Dest != "EGLL" {
		t.Errorf("take-off: %+v", o)
	}
	if o = dec("/ILNGE7X.TOF005AC C-FNOH2ACA23     261026220430CYVRRJBB-004050295806374000013000022447M85P04B205B205040600092122M32P08      2462M81P05  2462M82P05  -08129681+04918982-12320592057NQVK1UNANSH1SVRVT8:::0FF0HGG00000481KQ1027700U00K02618S00H00800Q0LN0005P10V10Q00069L99K69SA9TP9RO9Q99MR9NO9N21NU01A0310V70TP0PE0PH0PI0NO      01OF0V304P31TJ1FPQ0V70G808O11AP0391DF93G9300AJR0RI1RS1TK1T9A1G0103N91O00UN6IR1LU6MI0SC0TO10QU01O1BL1K10V901G13B19D13019T0V70RF0000K4A0K4D0JLQ::::::::::::::::::::::)))2B0N10Q1P0E0U1G120692U9OF0SF0SF12512B0T30UB2CP08P0TM12I04R01I0000050002500O004400O000008000000190002000:::00000060000001742002081000FK6509T010102000001070007661I9GT9270HD10GU00027T1103T1LC6P81KL2VBR979AA        0OA70P00L1Q0000").Fields["ops"].(OpsReport); o.OffAt != "2026-10-26T22:04:30Z" {
		t.Errorf("Air Canada take-off: %+v", o)
	}
	// an engine with no position fix reports zeros: that is no position, not a point off Africa
	if r = dec("/ILNGE7X.CLB006UA N249932UAL27     261026010104KDENEGLL-072060295897486000013000022447M85P04B205B205040601092122M32P08      2462M81P05  2462M82P05  -00331855+00000000+00000000007NQVK1UNANS01SVRVL8:::0FF0HGG00000481O810BHE01201301301H00K00K00B0000007S10P10Q00008JU8KE8QP8Q68NV8NB8IJ8JT8KA1R801M02L0V70PS6FA6GG6GN0P00NE0MQ031B0V80C4320G1GMS0VC0DA0E510IB03E0VQ7UJ7V50A4K0R21Q61A9130A14K103061OB0TC6CP1GK63G0Q90TR10R401J1FC1EQ0V009114P19C14S19E0VA0P30000AM80AMI0A7R::::::::::::::::::::::)))131620A0Q16151S1Q0481O9QV0AQ0AR12312B0PG0TM1EH0E60TR10H03D01I0000050004500O004400O:00000190002000:::000000600000017426G2081000D3FT9IS010102000002020103551Q9S99RN9H96731FH0OA70P00L1Q0001007NQVK1UNANS01SVRVL8:::0FF0HGG00000481PQ10LGT00P00O0190BD00E00D00J0000029E10Q10Q00009DF9GH9IV9G19CG9C79B09E49CC1T201L02N0V70QI6F96FO6G00P00NE0N404D70V50CD31O31GMN0V60F00KG10PU03D0L297D9750AE70SH1QA14K164I0S4103FI1O70PP6CU1JH6LQ0J90Q410R401J1IN1GS0V801G15I1BI1621C10V80RQ0020BSF0BSJ0BQ8::::::::::::::::::::::)))161H10F150M0Q1917049739OF0UI0UI12212B0U40U71EC0KG0Q40PG02B01I0000050004500G004400G000002000000190002000:::00000060000001742002081000D16AJI5000002000004020002611I9RE9R0A116O21HO0OA70P00L1Q0001//0S40SC0SR0T00T90VG10110910B///0U70UH0UT0VF0VF10E11811N12D///01B01E01G01H01V02102602G02L///0TR0TI0S80QE0RA0P40N50M70J5///0RJ0RL0RM0RA0RH0T20TA0T80SI/((IOI7IVINIUJFJMJBIV(//00102803K05S0A70D00G810S16M/06TN"); r.Pos != nil {
		t.Errorf("zero fix: %+v", r.Pos)
	}
	// an arrival's reports name the airport twice: it is the destination only
	if o = dec("<202>JEG\r\n9  C-FNOHACA20     TO 44725/10/2601:16:44CYVRCYVR\r\n 1664B").Fields["ops"].(OpsReport); o.Origin != "" || o.Dest != "CYVR" {
		t.Errorf("same airport twice: %+v", o)
	}
}

// A message nothing decodes is still described as well as its form allows, and readable text is listed as text. Real messages.
func TestDescribeWhatWeCannotDecode(t *testing.T) {
	for _, c := range []struct{ label, dir, text, kind, want string }{
		{"7B", "down", "CX85\r\nEASTERN GREENLAND ROUTING\r\n64N040W 74N020W 78N000E 80N020E 80N030E OPIREL\r\nCHEERS\r\n843", "text_message", "Crew message: CX85 / EASTERN GREENLAND ROUTING / 64N040W 74N020W 78N000E 80N020E 80N030E OPIREL / CHEERS / 843"},
		{"RA", "up", ".HDQONQF ~6\r\n06:45 TAFOR\r\nTAF WSSS 270500Z 2706/2812 10007KT 9999 FEW015 SCT020=\r\n\r\nTAF WADD 270500Z 2706/2812 13009KT 9999 SCT016=\r\n\r\nTAF WIII 270500Z 2706/2812 34011KT 7000 SCT020\r\nBECMG 2709/2711 20004KT\r\nBECMG 2803/2805 32010KT=\r\n", "text_message", "Forecast (TAF): .HDQONQF ~6 / 06:45 TAFOR / TAF WSSS 270500Z 2706/2812 10007KT 9999 FEW015 SCT020= / TAF WADD 270500Z 2706/281…"},
		{"H1", "down", "RPT13;PG1;REAL;SW030M2G\r\n1  \r\n2  \r\n3  -1.5;+2.3;312\r\n4  32000\r\n5  -0.1;-0.2;-0.1;-0.1;-0.1\r\n6  +1.8;+2.1;+2.1;+2.1;+2.1\r\n7  0.0;-0.1;-0.1;-0.1;-0.2\r\n8  0.0;+3.5;0.0;0.0;+3.5;0.0\r\n9  0.0;+3.5;0.0;0.0;+3.4;0.", "unknown", "Real-time system report RPT13 (label H1, not decoded)"},
		{"H1", "down", "CFG422000 1 24OCT26 1309 UA N24993 UAL178 LIRF/KDEN BCG47-45LD-0080 C L\r\nLRU Left 1 General Processor Module\r\nHW\r\nHWPN 182513-002 \r\nHWML 00                            \r\nHWSN 230930339\r\nSW\r\nLOC LS1315765 CCR", "unknown", "Aircraft configuration report (label H1, not decoded)"},
		{"H1", "down", "A380000524,1,1,TB000000;REP044,03;H0104403400000214.HL762610271026142322081;H02RKSI KLAXAAR202    S0586S0785CMS801901A;H03no threshold is exceeded                                          ;WSCS10;WSCS21;RVD", "unknown", "A380 maintenance report REP044 (label H1, not decoded)"},
		{"H1", "down", "          HAM53-21CS-2024\r\nSW6 42 CCS HOSTED APPS OSS         BCG5B-CCS0-04K1\r\nSW7 21 PECS OPS                    HAM55-21PE-2014\r\nSW8 24 PDMD AMI                    BCG4B-7870-0014\r\nSW9 24 PDOM OPS        ", "unknown", "Software and hardware status table (label H1, not decoded)"},
		{"H1", "down", "RESREQ/AK,1158AF6", "unknown", "Acknowledgement (label H1, not decoded)"},
		{"4C", "up", "-FSQ CA01", "unknown", "Air Canada company message FSQ (no text) (label 4C, not decoded)"},
		{"H1", "down", "PRG/FNDAL946/DTKLAX,24R,76,002900,29B4DA", "unknown", "Progress report DAL946: to KLAX, runway 24R (label H1, not decoded)"},
		{"H1", "down", "021 641\r\n958959  92261058 8031 64835990\r\n958974  92251060 8112 64855990\r\n 44 41 36700 76300 37000  15 6673 10553 10584\r\n 37 42 36600 55543 36900\r\n  43CLS)505800355800 00)4855472330 000 000 000\r\n 615 609 815", "unknown", "Numeric data block (label H1, 192 characters, not decoded)"},
		{"H1", "down", ".BLVBOCR 240501\r\nDFD\r\nAN N393DA\r\n- #DFXCM 99 2FSAHMRN 0", "unknown", "Ground request for aircraft data (label H1, not decoded)"},
		{"H1", "down", "................................................\r\n.......................................................\r\n.......................................................\r\n..........................................", "unknown", "Blank or padding data (label H1)"},
		{"4P", "up", "-// TXN PA01         25OCT/0740          C-FNOH/833/AC0020\n\tDFR AC0020/25\n\t DFR AC0020/25\n\t\n\tA/C   DG    FM  DEP   VAR     TO   AG   ARR   VAR       PAX        \n\t833   E20  SIN 0039* 00:01E   YVR  D69 1532  00:47L    29/21/210 \n\t\n\tA/C 833D    TURN FM 0019/23OCT TA 03:39*\n\t            TURN TO 0025/25OCT TA 03:53\n\t\n\t\n\t***ALL TIMES ABOVE ARE UTC***\n\t", "unknown", "Air Canada TXN message (label 4P, not decoded)"},
		{"4R", "down", "C32APO         1APUON 833 1524Z 25/0074/P11", "unknown", "Air Canada aircraft event message (label 4R, not decoded)"},
		{"37", "up", "NNSDFXY5X  OPTICLIMB\n\t***OPTICLIMB PROFILE***\n\tN575UP  UPS81  PVG-ANC\n\t   DATE: 2026-10-29\n\t\n\t   THIS FLIGHT IS NOT\n\t   SELECTED FOR AN\n\t   OPTICLIMB PROFILE.", "text_message", "Message to the aircraft: NNSDFXY5X OPTICLIMB / ***OPTICLIMB PROFILE*** / N575UP UPS81 PVG-ANC / DATE: 2026-10-29 / THIS FLIGHT IS NOT /…"},
		{"25", "down", "42 PLND PUSH UPDATE\r\nDL2947/24 PDX-LAX\r\nSHIP-3723\r\nSKD PUSH  - 0705L/1405Z\r\nPLND PUSH - 0700L/1400Z\r\nSKD ETA   - 0935L/1635Z\r\nPLND ETA  - 0922L/1622Z", "text_message", "Push update: 42 PLND PUSH UPDATE / DL2947/24 PDX-LAX / SHIP-3723 / SKD PUSH - 0705L/1405Z / PLND PUSH - 0700L/1400Z / SKD E…"},
		{"5Z", "down", "/B6 LDG DATA REQ   / KLAX KEWR 25 073253 KEWR R22L G4486", "text_message", "Message from the aircraft: /B6 LDG DATA REQ / KLAX KEWR 25 073253 KEWR R22L G4486"},
		{"VA", "up", "FT1131550XS", "unknown", "Data message (label VA, 11 characters, not decoded)"},
		{"25", "down", "42 PLND PUSH UPDATE\r\nDL2947/24 PDX-LAX\r\nSHIP-3723\r\nSKD PUSH  - 0705L/1405Z", "text_message", "Push update: 42 PLND PUSH UPDATE / DL2947/24 PDX-LAX / SHIP-3723 / SKD PUSH - 0705L/1405Z"},
	} {
		d := Unknown
		switch c.dir {
		case "up":
			d = Uplink
		case "down":
			d = Downlink
		}
		r, err := Decode(Message{Label: c.label, Dir: d, Text: c.text})
		if err != nil || string(r.Kind) != c.kind || r.Summary != c.want {
			t.Errorf("%s %.40q: %v %q, want %s %q", c.label, c.text, r.Kind, r.Summary, c.kind, c.want)
		}
	}
	// readable text keeps its lines for the expanded view
	r, _ := Decode(Message{Label: "7B", Dir: Downlink, Text: "CX85\r\nEASTERN GREENLAND ROUTING\r\n64N040W 74N020W\r\nCHEERS\r\n843"})
	if r.Kind != KindText || !strings.Contains(r.Text, "EASTERN GREENLAND ROUTING\n64N040W 74N020W\nCHEERS") {
		t.Errorf("text keeps its lines: %v %q", r.Kind, r.Text)
	}
	// a message of a few characters is not text
	if r, _ = Decode(Message{Label: "H1", Text: "OK HI"}); r.Kind != KindUnknown {
		t.Errorf("too short for text: %v", r.Kind)
	}
}

// Oceanic clearances: the crew's request and the centre's reply. Real messages.
func TestOceanicClearance(t *testing.T) {
	r, err := Decode(Message{Label: "B1", Text: "/EGGX.OC1/RCL 030\r\nUAL26-ATSIX/1316 M085F3706D8D"})
	c, _ := r.Fields["clearance"].(Clearance)
	if err != nil || r.Kind != KindClearance || c.What != "request" || c.Callsign != "UAL26" || c.Entry != "ATSIX" || c.EntryTime != "1316" || c.Level != 370 || c.Mach != 0.85 {
		t.Fatalf("request: %v %+v", r.Kind, c)
	}
	if want := "Oceanic clearance request UAL26: enter at ATSIX 13:16Z, FL370 M0.85"; r.Summary != want || r.Dir != Downlink || r.Identity.Flight != "UA26" {
		t.Errorf("request summary %q, %v, %q", r.Summary, r.Dir, r.Identity.Flight)
	}

	r, err = Decode(Message{Label: "B2", Text: "/PIKCLYA.OC1/CLA 1217 261027 EGGX CLRNCE 173\r\nUAL26 CLRD TO KDEN VIA ATSIX\r\nNAT B\r\nATSIX 62N020W 63N030W 63N040W\r\n62N050W PIDSO\r\nFM ATSIX/1316 MNTN F370 M085\r\nEND OF MESSAGE3732"})
	c, _ = r.Fields["clearance"].(Clearance)
	if err != nil || r.Kind != KindClearance || c.What != "clearance" || c.Centre != "EGGX" || c.Number != "173" || c.Dest != "KDEN" || c.Track != "NAT B" ||
		c.Entry != "ATSIX" || c.EntryTime != "1316" || c.Level != 370 || c.Mach != 0.85 || c.IssuedAt != "2026-10-27T12:17:00Z" || r.Dir != Uplink {
		t.Fatalf("clearance: %v %+v", r.Kind, c)
	}
	names := ""
	for _, f := range c.Route.Fixes {
		names += f.Name + " "
	}
	if names != "ATSIX 62N020W 63N030W 63N040W 62N050W PIDSO " {
		t.Errorf("route %q", names)
	}
	if f := c.Route.Fixes[1]; !f.HasPos || f.Lat != 62 || f.Lon != -20 {
		t.Errorf("coordinates: %+v", f)
	}
	if f := c.Route.Fixes[0]; f.HasPos {
		t.Errorf("a named fix has no coordinates of its own: %+v", f)
	}
	if want := "Oceanic clearance UAL26 to KDEN: NAT B, ATSIX 62N020W 63N030W 63N040W 62N050W PIDSO (entry ATSIX 13:16Z, FL370 M0.85)"; r.Summary != want {
		t.Errorf("summary %q", r.Summary)
	}
	// something else on the same address, or a clearance that does not fit the form, is left alone
	for _, s := range []string{"/EGGX.OC1/RCL 030 nonsense", "/EGGX.OC1/CLA 1217 261027 EGGX CLRNCE 173 UAL26 CLRD TO KDEN MNTN F370 M085 END OF MESSAGE0000"} {
		if r, _ = Decode(Message{Label: "A9", Dir: Uplink, Text: s}); r.Kind == KindClearance {
			t.Errorf("%q decoded as a clearance", s)
		}
	}
}
