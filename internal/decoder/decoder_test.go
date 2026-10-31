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
	if r.Kind != KindUnknown || !strings.HasPrefix(r.Summary, "Undecoded H1 message: 239N393DA") || r.Pos != nil {
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
	for _, s := range []string{"- #MDPWI/WD370,N25000W180000,028055,370M47.", "- #MDREQPER9CB5", "plain text #M1B/ not a label"} {
		if r, _ := Decode(Message{Label: "H1", Text: s}); r.Kind != KindUnknown {
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
