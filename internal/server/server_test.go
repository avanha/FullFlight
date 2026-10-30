package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/navdata"
	"github.com/avanha/fullflight/internal/store"
	"github.com/avanha/fullflight/internal/watchlist"
)

func setup(t *testing.T, pw string) *httptest.Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	ts := httptest.NewServer((&Server{Store: st, Username: "me", Password: pw, Now: func() time.Time { return now }}).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func TestWatchAPI(t *testing.T) {
	ts := setup(t, "")
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"UA869 2026-10-31"}`)
	if code != 201 || !strings.Contains(body, `"status":"waiting"`) || !strings.Contains(body, `"ident":"UA869"`) {
		t.Fatalf("add flight: %d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"B-LRM","until":"2026-11-05T00:00:00Z"}`)
	if code != 201 || !strings.Contains(body, `"end":"2026-11-05T00:00:00Z"`) {
		t.Fatalf("add tail: %d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"UA869 someday"}`)
	if code != 400 || !strings.Contains(body, "YYYY-MM-DD") {
		t.Errorf("bad date: %d %s", code, body)
	}
	code, body = do(t, "GET", ts.URL+"/api/watches", "")
	if code != 200 || strings.Count(body, `"id"`) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ = do(t, "DELETE", ts.URL+"/api/watches/1", ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if _, body = do(t, "GET", ts.URL+"/api/watches", ""); strings.Count(body, `"id"`) != 1 {
		t.Errorf("after delete: %s", body)
	}
	if code, body = do(t, "GET", ts.URL+"/api/track/a1b2c3?hours=6", ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("track: %d %s", code, body)
	}
	if code, body = do(t, "GET", ts.URL+"/", ""); code != 200 || !strings.Contains(body, "leaflet") {
		t.Errorf("index: %d", code)
	}
}

func TestWatchDestinationAPI(t *testing.T) {
	ts := setup(t, "")
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"UA869 2026-10-31 sfo"}`)
	if code != 201 || !strings.Contains(body, `"dest":"KSFO"`) || !strings.Contains(body, "San Francisco") {
		t.Fatalf("%d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"UA870 2026-10-31 ZZZ"}`)
	if code != 400 || !strings.Contains(body, "unknown airport") {
		t.Errorf("unknown airport: %d %s", code, body)
	}
}

func TestMessagesEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	lat, lon := 27.6, 152.6
	pt := now.Add(-time.Hour - 2*time.Second)
	for i, ago := range []time.Duration{3 * time.Hour, time.Hour} { // inserted out of order is fine
		st.AddMessage(store.Message{ID: int64(i + 1), Hex: "780AB8", T: now.Add(-ago), Label: "B6", Text: "t" + string(rune('a'+i)),
			Kind: "adsc", Summary: "ADS-C report", Lat: &lat, Lon: &lon, PosT: &pt, Route: `[{"Lat":26.9,"Lon":150,"Kind":"next"}]`,
			Mismatch: "tail: x"})
	}
	st.AddMessage(store.Message{ID: 9, Hex: "780AB8", T: now.Add(-100 * time.Hour), Label: "SA", Text: "old"}) // outside 48 h
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	code, body := do(t, "GET", ts.URL+"/api/messages/780ab8", "")
	if code != 200 || strings.Count(body, `"raw"`) != 2 || strings.Index(body, `"raw":"ta"`) > strings.Index(body, `"raw":"tb"`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, want := range []string{`"summary":"ADS-C report"`, `"route":[{"lat":26.9,"lon":150,"kind":"next"}]`, `"mismatch":"tail: x"`, `"lat":27.6`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if _, body = do(t, "GET", ts.URL+"/api/messages/780AB8?hours=200", ""); strings.Count(body, `"raw"`) != 3 {
		t.Errorf("hours=200: %s", body)
	}
	if _, body = do(t, "GET", ts.URL+"/api/messages/780AB8?limit=1", ""); strings.Count(body, `"raw"`) != 1 || !strings.Contains(body, `"raw":"tb"`) {
		t.Errorf("limit=1 should keep the newest: %s", body)
	}
}

func TestAirframesEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "airframes.io", LastSeen: time.Unix(1793000000, 0)})
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	code, body := do(t, "GET", ts.URL+"/api/airframes", "")
	if code != 200 || !strings.Contains(body, `"hex":"780AB8"`) || !strings.Contains(body, `"tail":"B-LRM"`) {
		t.Errorf("%d %s", code, body)
	}
}

func TestAuth(t *testing.T) {
	ts := setup(t, "secret")
	if code, _ := do(t, "GET", ts.URL+"/api/watches", ""); code != 401 {
		t.Errorf("no creds: %d", code)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/watches", nil)
	req.SetBasicAuth("me", "secret")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("good creds: %d", resp.StatusCode)
	}
	req.SetBasicAuth("me", "wrong")
	if resp, _ = http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Errorf("bad creds: %d", resp.StatusCode)
	}
}

func TestWatchShowsLastPositionAndType(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	e.Hex = "780AB8"
	st.AddWatch(e)
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "adsb.lol", LastSeen: now})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/watches", "")
	if strings.Contains(body, "last_pos") || !strings.Contains(body, `"type":"A359"`) {
		t.Errorf("before any position: %s", body)
	}

	alt := 38000.0
	st.AddPosition(store.Position{Hex: "780AB8", T: now.Add(-10 * time.Minute), Lat: 40, Lon: -100, AltFt: &alt, Source: "adsb.lol"})
	_, body = do(t, "GET", ts.URL+"/api/watches", "")
	if !strings.Contains(body, `"kind":"ADS-B"`) || !strings.Contains(body, `"t":"2026-10-30T17:50:00Z"`) {
		t.Errorf("ADS-B only: %s", body)
	}

	// A later ADS-C report wins; a later but suspect one is ignored.
	lat, lon := 45.0, -150.0
	pt := now.Add(-3 * time.Minute)
	st.AddMessage(store.Message{ID: 1, Hex: "780AB8", T: now.Add(-2 * time.Minute), Label: "B6", Text: "a", Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &pt})
	bad := now.Add(-time.Minute)
	st.AddMessage(store.Message{ID: 2, Hex: "780AB8", T: now, Label: "BA", Text: "b", Kind: "cpdlc", Lat: &lat, Lon: &lon, PosT: &bad, Suspect: "implies 900 kt"})
	_, body = do(t, "GET", ts.URL+"/api/watches", "")
	if !strings.Contains(body, `"kind":"ADS-C"`) || !strings.Contains(body, `"t":"2026-10-30T17:57:00Z"`) || strings.Contains(body, "CPDLC") {
		t.Errorf("ADS-C later than ADS-B: %s", body)
	}
}

func TestMessagesFlagUndecoded(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	st.AddMessage(store.Message{ID: 1, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "H1", Text: "x1", Kind: "unknown", Summary: "Undecoded H1 message: 239N393DA"})
	st.AddMessage(store.Message{ID: 2, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "MA", Text: "x2", Kind: "miam", Summary: "MIAM (not decoded)"})
	st.AddMessage(store.Message{ID: 3, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "BA", Text: "x3", Kind: "cpdlc", Summary: "CPDLC downlink: WILCO"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	st.AddMessage(store.Message{ID: 4, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "H1", Text: "x4", Kind: "fms_position", Summary: "FMS", TimeNote: "arrived 1 Z", TimeFlag: true})
	_, body := do(t, "GET", ts.URL+"/api/messages/A48E3A", "")
	if strings.Count(body, `"time_unverified":true`) != 1 || !strings.Contains(body, `"time_note":"arrived 1 Z"`) {
		t.Errorf("time fields: %s", body)
	}
	if strings.Count(body, `"undecoded":true`) != 2 {
		t.Errorf("want 2 undecoded: %s", body)
	}
}

func TestTrackCarriesSpeedHeadingAndVerticalRate(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	alt, gs, trk, vr := 35025.0, 480.5, 135.0, -1800.0
	st.AddPosition(store.Position{Hex: "A48E3A", T: now.Add(-time.Minute), Lat: 40, Lon: -100, AltFt: &alt, GS: &gs, Track: &trk, VRate: &vr, Callsign: "DAL1317", Source: "adsb.lol"})
	st.AddPosition(store.Position{Hex: "A48E3A", T: now.Add(-30 * time.Second), Lat: 40.1, Lon: -100, OnGround: true, Source: "adsb.lol"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/track/A48E3A", "")
	for _, want := range []string{`"gs":480.5`, `"track":135`, `"vrate":-1800`, `"alt_ft":35025`, `"on_ground":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("track JSON missing %s: %s", want, body)
		}
	}
}

func TestWatchShowsReportedRoute(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	type rep struct {
		age      time.Duration
		from, to string
	}
	// watches returns /api/watches for an aircraft that has sent the given REP081 reports.
	watches := func(reports ...rep) string {
		st, _ := store.Open(":memory:")
		defer st.Close()
		e, _ := watchlist.Parse("B-LRM", now, time.Time{})
		e.Hex = "780AB8"
		st.AddWatch(e)
		for i, r := range reports {
			at := now.Add(-r.age)
			st.AddMessage(store.Message{ID: int64(i + 1), Hex: "780AB8", T: at, PosT: &at, Label: "MA", Text: fmt.Sprint("t", i), Kind: "miam",
				Decoded: `{"fields":{"rep081":{"Origin":"` + r.from + `","Dest":"` + r.to + `"}}}`})
		}
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
		defer ts.Close()
		_, b := do(t, "GET", ts.URL+"/api/watches", "")
		return b
	}

	if b := watches(); strings.Contains(b, `"route"`) {
		t.Errorf("route shown before any REP081: %s", b)
	}
	// Fresh: shown with airport names, not stale.
	b := watches(rep{12 * time.Minute, "VHHH", "YSSY"})
	for _, want := range []string{`"from":"VHHH"`, `"to":"YSSY"`, `"to_name":"Sydney`, `"from_name":"Hong Kong`, `"source":"REP081"`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(b, `"stale"`) {
		t.Errorf("a 12 minute old report is stale: %s", b)
	}
	// The freshness boundary.
	if b = watches(rep{InfoFreshFor - time.Minute, "VHHH", "EDDM"}); !strings.Contains(b, `"to":"EDDM"`) || strings.Contains(b, `"stale"`) {
		t.Errorf("just inside the window: %s", b)
	}
	if b = watches(rep{InfoFreshFor + time.Minute, "VHHH", "EDDM"}); !strings.Contains(b, `"to":"EDDM"`) || !strings.Contains(b, `"stale":true`) {
		t.Errorf("just outside the window: %s", b)
	}
	// The newest report is the current destination (the aircraft has turned round).
	b = watches(rep{3 * time.Hour, "YSSY", "VHHH"}, rep{40 * time.Minute, "VHHH", "YSSY"})
	if !strings.Contains(b, `"from":"VHHH"`) || !strings.Contains(b, `"to":"YSSY"`) || strings.Contains(b, `"stale"`) {
		t.Errorf("newest should win: %s", b)
	}
	// Only an old one: shown, but flagged as possibly out of date.
	if b = watches(rep{3 * time.Hour, "VHHH", "YSSY"}); !strings.Contains(b, `"to":"YSSY"`) || !strings.Contains(b, `"stale":true`) {
		t.Errorf("3 hour old report: %s", b)
	}
	// Too old to be worth showing at all.
	if b = watches(rep{InfoHideAfter + time.Hour, "VHHH", "YSSY"}); strings.Contains(b, `"route"`) {
		t.Errorf("a day-old report still shown: %s", b)
	}
	// An unknown airport code is still shown, just without a name.
	if b = watches(rep{time.Minute, "VHHH", "ZZZZ"}); !strings.Contains(b, `"to":"ZZZZ"`) || strings.Contains(b, `"to_name"`) {
		t.Errorf("unknown airport: %s", b)
	}
}

func TestWatchShowsCurrentFlight(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	type seed struct {
		adsb     string // callsign on an ADS-B position
		adsbAgo  time.Duration
		flight   string // flight number in a message
		callsign string // callsign in a message (AFN)
		msgAgo   time.Duration
		mismatch string
	}
	watches := func(kind string, seeds ...seed) string {
		st, _ := store.Open(":memory:")
		defer st.Close()
		e, _ := watchlist.Parse("B-LRM", now, time.Time{})
		if kind == "flight" {
			e, _ = watchlist.Parse("CPA113", now, time.Time{})
		}
		e.Hex = "780AB8"
		st.AddWatch(e)
		for i, sd := range seeds {
			if sd.adsb != "" {
				st.AddPosition(store.Position{Hex: "780AB8", T: now.Add(-sd.adsbAgo), Lat: float64(i + 1), Lon: 1, Callsign: sd.adsb, Source: "adsb.lol"})
			}
			if sd.flight != "" || sd.callsign != "" {
				st.AddMessage(store.Message{ID: int64(i + 1), Hex: "780AB8", T: now.Add(-sd.msgAgo), Label: "H1", Text: fmt.Sprint("t", i),
					Flight: sd.flight, Callsign: sd.callsign, Mismatch: sd.mismatch})
			}
		}
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
		defer ts.Close()
		_, b := do(t, "GET", ts.URL+"/api/watches", "")
		return b
	}
	min := time.Minute

	if b := watches("tail"); strings.Contains(b, `"flight":{`) {
		t.Errorf("flight with no data: %s", b)
	}
	// From ADS-B: both forms, not stale.
	b := watches("tail", seed{adsb: "CPA113", adsbAgo: 20 * time.Second})
	for _, want := range []string{`"callsign":"CPA113"`, `"number":"CX113"`, `"source":"ADS-B"`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(b, `"stale"`) {
		t.Errorf("fresh ADS-B callsign called stale: %s", b)
	}
	// From a message's flight number (IATA form): the ICAO callsign is worked out.
	b = watches("tail", seed{flight: "CX113", msgAgo: 5 * min})
	if !strings.Contains(b, `"callsign":"CPA113"`) || !strings.Contains(b, `"number":"CX113"`) || !strings.Contains(b, `"source":"messages"`) {
		t.Errorf("from message flight: %s", b)
	}
	// From an AFN callsign (ICAO form): the flight number is worked out.
	if b = watches("tail", seed{callsign: "CPA833", msgAgo: 5 * min}); !strings.Contains(b, `"number":"CX833"`) {
		t.Errorf("from AFN callsign: %s", b)
	}
	// Whichever is newer wins, in either direction.
	if b = watches("tail", seed{adsb: "CPA843", adsbAgo: 2 * time.Hour}, seed{flight: "CX113", msgAgo: 5 * min}); !strings.Contains(b, `"number":"CX113"`) || strings.Contains(b, "CX843") {
		t.Errorf("newer message should win: %s", b)
	}
	if b = watches("tail", seed{adsb: "CPA843", adsbAgo: 30 * time.Second}, seed{flight: "CX113", msgAgo: 5 * min}); !strings.Contains(b, `"number":"CX843"`) || !strings.Contains(b, `"source":"ADS-B"`) {
		t.Errorf("newer ADS-B should win: %s", b)
	}
	// Staleness: three hours old is shown but flagged; a day and more is dropped.
	if b = watches("tail", seed{flight: "CX113", msgAgo: 3 * time.Hour}); !strings.Contains(b, `"number":"CX113"`) || !strings.Contains(b, `"stale":true`) {
		t.Errorf("3 hours: %s", b)
	}
	if b = watches("tail", seed{flight: "CX113", msgAgo: InfoHideAfter + time.Hour}); strings.Contains(b, `"flight":{`) {
		t.Errorf("a day old: %s", b)
	}
	// A message whose text names another aircraft does not give this aircraft its flight.
	if b = watches("tail", seed{flight: "CX999", msgAgo: min, mismatch: "text names B-LRD, which is hex 780AA4, not 780AB8"}); strings.Contains(b, `"flight":{`) {
		t.Errorf("another aircraft's flight: %s", b)
	}
	// An airline we have no table entry for is shown as received.
	if b = watches("tail", seed{adsb: "ZZZ123", adsbAgo: 10 * time.Second}); !strings.Contains(b, `"callsign":"ZZZ123"`) || strings.Contains(b, `"number"`) {
		t.Errorf("unknown airline: %s", b)
	}
	// A flight watch already is a flight, so it gets no flight line.
	if b = watches("flight", seed{adsb: "CPA113", adsbAgo: 10 * time.Second}); strings.Contains(b, `"flight":{`) {
		t.Errorf("flight watch has a flight line: %s", b)
	}
}

func TestMessagesCarryWeatherSpeedAndWaypoints(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 19, 0, 0, 0, time.UTC)
	pos := time.Date(2026, 10, 30, 18, 48, 0, 0, time.UTC)
	lat, lon := -5.46, 141.742
	wd, wk, wt, gs, trk := 148.0, 34.0, -51.0, 520.0, 122.0
	fl, _ := json.Marshal(store.FixList{GS: &gs, Trk: &trk, List: []store.FixEntry{
		{Name: "NEXTX", Role: "next", T: time.Date(2026, 10, 30, 18, 55, 0, 0, time.UTC).Unix()},
		{Name: "KADAB", Role: "then"}, // the 2013 database has it behind the aircraft
	}})
	st.AddMessage(store.Message{ID: 1, Hex: "780AA4", T: pos.Add(-3 * time.Minute), Label: "BA", Text: "a", Kind: "cpdlc", Summary: "CPDLC position report",
		Lat: &lat, Lon: &lon, PosT: &pos, WxDir: &wd, WxKt: &wk, WxTemp: &wt, GS: &gs, Trk: &trk, Fixes: string(fl)})
	st.AddMessage(store.Message{ID: 2, Hex: "780AA4", T: pos, Label: "SA", Text: "b", Kind: "media_advisory", Summary: "Link"}) // no weather

	nav := navdata.New()
	nav.Add("NEXTX", navdata.Point{Lat: -6.0, Lon: 142.7, Kind: "fix"})
	nav.Add("KADAB", navdata.Point{Lat: -4.965, Lon: 141.0, Kind: "fix"})
	ts := httptest.NewServer((&Server{Store: st, Nav: nav, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/messages/780AA4", "")
	var ms []struct {
		ID int64 `json:"id"`
		Wx *struct {
			Dir, Kt *float64
			TempC   *float64 `json:"temp_c"`
		} `json:"wx"`
		GS, Trk   *float64
		Waypoints []struct {
			Name, Role, Source string
			Exact              bool
			Lat                float64
		} `json:"waypoints"`
	}
	if err := json.Unmarshal([]byte(body), &ms); err != nil || len(ms) != 2 {
		t.Fatalf("%v: %s", err, body)
	}
	a, b := ms[0], ms[1]
	if a.Wx == nil || *a.Wx.Dir != 148 || *a.Wx.Kt != 34 || *a.Wx.TempC != -51 || *a.GS != 520 || *a.Trk != 122 {
		t.Errorf("weather/motion = %+v gs=%v trk=%v", a.Wx, a.GS, a.Trk)
	}
	if len(a.Waypoints) != 1 || a.Waypoints[0].Name != "NEXTX" || !a.Waypoints[0].Exact || a.Waypoints[0].Source != "database" || a.Waypoints[0].Lat != -6.0 {
		t.Errorf("waypoints = %+v (KADAB must not be placed: the database has it behind the aircraft)", a.Waypoints)
	}
	if b.Wx != nil || b.GS != nil || len(b.Waypoints) != 0 {
		t.Errorf("a message without them: %+v", b)
	}
}

func TestRouteWaypointsAreNamedWhenWeKnowThem(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 19, 0, 0, 0, time.UTC)
	lat, lon := -4.54, 140.25
	pt := now.Add(-time.Hour)
	// An ADS-C report predicting: a waypoint we learned the name of, one only the database knows, one nobody knows,
	// and the flight computer's projection (never named). The first row predates Kind, as an old stored route would.
	route := `[{"Lat":-5.233,"Lon":140.798,"ETASec":443},{"Lat":-6.152,"Lon":142.008,"Kind":"next+1"},{"Lat":-7.5,"Lon":143.5,"Kind":"next+1"},{"Lat":-5.233,"Lon":140.798,"ETASec":2220,"Kind":"fixed"}]`
	st.AddMessage(store.Message{ID: 1, Hex: "780AA4", T: pt, Label: "B6", Text: "a", Kind: "adsc", Lat: &lat, Lon: &lon, PosT: &pt, Route: route})
	st.AddFix("OLGAN", 9, -5.233, 140.798, "adsc-eta")
	nav := navdata.New()
	nav.Add("KAPPU", navdata.Point{Lat: -6.1521, Lon: 142.0079, Kind: "fix"})
	nav.Add("ELSEWHERE", navdata.Point{Lat: -5.233, Lon: 140.798, Kind: "fix"}) // also here, but learned names win
	ts := httptest.NewServer((&Server{Store: st, Nav: nav, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/messages/780AA4", "")
	var ms []struct {
		Route []routeWpJSON `json:"route"`
	}
	if err := json.Unmarshal([]byte(body), &ms); err != nil || len(ms) != 1 || len(ms[0].Route) != 4 {
		t.Fatalf("%v: %s", err, body)
	}
	r := ms[0].Route
	if r[0].Kind != "next" || r[0].Name != "OLGAN" || r[0].NameSrc != "learned" {
		t.Errorf("learned: %+v", r[0])
	}
	if r[1].Kind != "next+1" || r[1].Name != "KAPPU" || r[1].NameSrc != "database" {
		t.Errorf("database: %+v", r[1])
	}
	if r[2].Name != "" {
		t.Errorf("an unknown waypoint was named: %+v", r[2])
	}
	if r[3].Kind != "fixed" || r[3].Name != "" || *r[3].ETASec != 2220 {
		t.Errorf("the projection must not take a name: %+v", r[3])
	}
}

func TestFlightsEndpoint(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
	id := int64(0)
	add := func(at time.Time, flight string, kind string, pos bool, decoded string) {
		id++
		m := store.Message{ID: id, Hex: "780AB8", T: at, Label: "BA", Text: fmt.Sprint("t", id), Flight: flight, Kind: kind, Decoded: decoded}
		if pos {
			lat, lon := 10.0, 100.0
			m.Lat, m.Lon, m.PosT = &lat, &lon, &at
		}
		st.AddMessage(m)
	}
	for i := 0; i < 6; i++ { // yesterday's CX840, today's CX843 with a reported route
		add(now.Add(-30*time.Hour+time.Duration(i)*30*time.Minute), "CX840", "adsc", true, "")
		add(now.Add(-8*time.Hour+time.Duration(i)*30*time.Minute), "CX843", "adsc", true, "")
	}
	add(now.Add(-7*time.Hour), "", "miam", false, `{"fields":{"rep081":{"Origin":"KJFK","Dest":"VHHH"}}}`)
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/flights/780ab8", "")
	var got []struct {
		ID, Flight, Callsign, From, To string
		FromName                       string `json:"from_name"`
		ToName                         string `json:"to_name"`
		Positions                      int
		Identified, Latest             bool
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got) != 2 {
		t.Fatalf("%v: %s", err, body)
	}
	a, b := got[0], got[1]
	if a.Flight != "CX840" || a.Callsign != "CPA840" || a.Latest || !a.Identified || a.Positions != 6 {
		t.Errorf("first = %+v", a)
	}
	if b.Flight != "CX843" || !b.Latest || b.From != "KJFK" || b.To != "VHHH" || b.ToName == "" || b.FromName == "" {
		t.Errorf("second = %+v", b)
	}
	if !strings.HasPrefix(b.ID, "CX843@") {
		t.Errorf("id = %q", b.ID)
	}
	// a shorter window leaves yesterday out
	_, body = do(t, "GET", ts.URL+"/api/flights/780AB8?hours=12", "")
	if strings.Contains(body, "CX840") || !strings.Contains(body, "CX843") {
		t.Errorf("12 hours: %s", body)
	}
	if _, body = do(t, "GET", ts.URL+"/api/flights/FFFFFF", ""); strings.TrimSpace(body) != "[]" {
		t.Errorf("unknown aircraft: %s", body)
	}
}

func TestHowInputIsRead(t *testing.T) {
	ts := setup(t, "")
	cases := []struct {
		input, kind, ident string
	}{
		{"TZP1", "flight", "TZP1"},        // ZIPAIR: not an airline we know, still a callsign
		{"CKK624", "flight", "CKK624"},    // China Cargo
		{"tail:CKK624", "tail", "CKK624"}, // unless you say otherwise
		{"flight:CPA113", "flight", "CPA113"},
		{"hex:abc123", "hex", "ABC123"},
		{"B-LRM", "tail", "B-LRM"},
		{"N8538V", "tail", "N8538V"},
		{"AEE123", "hex", "AEE123"}, // six hex digits: an address unless you say flight:
	}
	for _, c := range cases {
		code, body := do(t, "POST", ts.URL+"/api/watches", fmt.Sprintf(`{"input":%q}`, c.input))
		if code != 201 || !strings.Contains(body, `"kind":"`+c.kind+`"`) || !strings.Contains(body, `"ident":"`+c.ident+`"`) {
			t.Errorf("%q: %d %s", c.input, code, body)
		}
	}
	// a flight whose airline we cannot turn into a callsign is refused now, with the way out
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"flight:CK624"}`)
	if code != 400 || !strings.Contains(body, `unknown airline code \"CK\"`) || !strings.Contains(body, "ICAO callsign") {
		t.Errorf("flight:CK624 = %d %s", code, body)
	}
	if code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"bad:XYZ"}`); code != 400 || !strings.Contains(body, "unknown prefix") {
		t.Errorf("bad prefix: %d %s", code, body)
	}
}
