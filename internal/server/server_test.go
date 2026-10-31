package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"flight:XQ624"}`)
	if code != 400 || !strings.Contains(body, `unknown airline code \"XQ\"`) || !strings.Contains(body, "ICAO callsign") {
		t.Errorf("flight:XQ624 = %d %s", code, body)
	}
	if code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"bad:XYZ"}`); code != 400 || !strings.Contains(body, "unknown prefix") {
		t.Errorf("bad prefix: %d %s", code, body)
	}
}

func TestNotesAPI(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	clock := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return clock }}).Handler())
	defer ts.Close()
	base := ts.URL + "/api/airframes/780ab8/notes" // lower case works

	if code, body := do(t, "GET", base, ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("no notes yet: %d %s", code, body)
	}
	code, body := do(t, "POST", base, `{"note":"The plane Yen took from SEA to HKG on 2026-10-27"}`)
	var a struct {
		ID      int64     `json:"id"`
		Hex     string    `json:"hex"`
		Note    string    `json:"note"`
		Created time.Time `json:"created"`
	}
	if err := json.Unmarshal([]byte(body), &a); err != nil || code != 201 || a.ID == 0 || a.Hex != "780AB8" || !a.Created.Equal(clock) ||
		a.Note != "The plane Yen took from SEA to HKG on 2026-10-27" {
		t.Fatalf("add: %d %s", code, body)
	}
	clock = clock.Add(time.Hour)
	do(t, "POST", base, `{"note":"Second note\nwith two lines"}`)

	_, body = do(t, "GET", base, "")
	var list []struct {
		ID   int64  `json:"id"`
		Note string `json:"note"`
	}
	json.Unmarshal([]byte(body), &list)
	if len(list) != 2 || list[0].Note != "Second note\nwith two lines" || list[1].ID != a.ID {
		t.Errorf("newest first, line breaks kept: %s", body)
	}

	// edit
	clock = clock.Add(time.Hour)
	code, body = do(t, "PUT", fmt.Sprintf("%s/%d", base, a.ID), `{"note":"Corrected: 2026-10-28"}`)
	if code != 200 || !strings.Contains(body, `"note":"Corrected: 2026-10-28"`) || !strings.Contains(body, `"created":"2026-10-30T12:00:00Z"`) || !strings.Contains(body, `"updated":"2026-10-30T14:00:00Z"`) {
		t.Errorf("edit: %d %s", code, body)
	}
	// refusals
	for _, c := range []struct {
		method, url, body string
		want              int
		msg               string
	}{
		{"POST", ts.URL + "/api/airframes/NOTHEX/notes", `{"note":"x"}`, 400, "six hex digits"},
		{"POST", base, `{"note":"   "}`, 400, "cannot be empty"},
		{"POST", base, `{"note":"` + strings.Repeat("x", store.MaxNoteBytes+1) + `"}`, 400, "too long"},
		{"POST", base, `not json`, 400, "bad JSON"},
		{"PUT", base + "/99999", `{"note":"x"}`, 404, "no such note"},
		{"PUT", base + "/abc", `{"note":"x"}`, 400, "bad note id"},
		{"PUT", fmt.Sprintf("%s/%d", ts.URL+"/api/airframes/780AA4/notes", a.ID), `{"note":"hijack"}`, 404, "no such note"}, // another airframe's note
		{"DELETE", fmt.Sprintf("%s/%d", ts.URL+"/api/airframes/780AA4/notes", a.ID), ``, 404, "no such note"},
		{"DELETE", base + "/99999", ``, 404, "no such note"},
	} {
		code, body := do(t, c.method, c.url, c.body)
		if code != c.want || !strings.Contains(body, c.msg) {
			t.Errorf("%s %s: %d %s, want %d %q", c.method, c.url, code, body, c.want, c.msg)
		}
	}
	// delete
	if code, _ := do(t, "DELETE", fmt.Sprintf("%s/%d", base, a.ID), ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if _, body = do(t, "GET", base, ""); strings.Contains(body, "Corrected") || !strings.Contains(body, "Second note") {
		t.Errorf("after delete: %s", body)
	}
}

func TestNotesAreVisibleOnWatchesAndTheAirframeList(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("B-LRM", now, time.Time{})
	e.Hex = "780AB8"
	st.AddWatch(e)
	e2, _ := watchlist.Parse("B-LRD", now, time.Time{})
	e2.Hex = "780AA4"
	st.AddWatch(e2)
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "x", LastSeen: now})
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-OLD", Source: "x", LastSeen: now.Add(-100 * time.Hour)})
	st.AddMessage(store.Message{ID: 1, Hex: "780AB8", T: now.Add(-time.Hour), Label: "BA", Text: "x"}) // heard from, so listed first
	st.AddNote("780AB8", "first", now)
	st.AddNote("780AB8", strings.Repeat("long ", 100), now.Add(time.Minute)) // newest, 500 characters
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/watches", "")
	var ws []struct {
		Ident    string
		Notes    int    `json:"notes"`
		LastNote string `json:"last_note"`
	}
	json.Unmarshal([]byte(body), &ws)
	by := map[string]int{}
	for i, w := range ws {
		by[w.Ident] = i
	}
	m, d := ws[by["B-LRM"]], ws[by["B-LRD"]]
	if m.Notes != 2 || !strings.HasPrefix(m.LastNote, "long long") || len([]rune(m.LastNote)) != 201 || !strings.HasSuffix(m.LastNote, "…") {
		t.Errorf("B-LRM: %d notes, preview of %d runes: %.30q", m.Notes, len([]rune(m.LastNote)), m.LastNote)
	}
	if d.Notes != 0 || d.LastNote != "" {
		t.Errorf("B-LRD has none: %+v", d)
	}
	if strings.Contains(body, `"notes":0`) {
		t.Errorf("zero counts should be left out: %s", body)
	}

	_, body = do(t, "GET", ts.URL+"/api/airframes", "")
	var af []struct {
		Hex   string `json:"hex"`
		Tail  string `json:"tail"`
		Tails []struct {
			Tail string `json:"tail"`
		} `json:"tails"`
		Notes   int  `json:"notes"`
		Watched bool `json:"watched"`
	}
	if err := json.Unmarshal([]byte(body), &af); err != nil || len(af) != 2 {
		t.Fatalf("%v: %s", err, body)
	}
	if af[0].Hex != "780AB8" || af[0].Tail != "B-LRM" || len(af[0].Tails) != 2 || af[0].Tails[1].Tail != "B-OLD" || af[0].Notes != 2 || !af[0].Watched {
		t.Errorf("780AB8 = %+v", af[0])
	}
	if af[1].Hex != "780AA4" || af[1].Tail != "" || af[1].Notes != 0 { // known only from its watch
		t.Errorf("780AA4 = %+v", af[1])
	}
}

func TestNotesNeedTheLogin(t *testing.T) {
	ts := setup(t, "secret")
	for _, c := range [][2]string{{"GET", "/api/airframes"}, {"GET", "/api/airframes/780AB8/notes"}, {"POST", "/api/airframes/780AB8/notes"}, {"DELETE", "/api/airframes/780AB8/notes/1"}} {
		if code, _ := do(t, c[0], ts.URL+c[1], `{"note":"x"}`); code != 401 {
			t.Errorf("%s %s without credentials = %d", c[0], c[1], code)
		}
	}
}

// Filed flight plans are served resolved to positions: coordinates as given, other fixes from the navigation
// database (the one nearest the point before, since names repeat), the airports at the ends; sent twice they are
// reported once; and the plan gives the watch its origin and destination.
func TestFlightPlanEndpointAndRoute(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	nav := navdata.New()
	nav.Add("TUNTO", navdata.Point{Lat: 25.0, Lon: 170.0, Kind: "fix"})
	nav.Add("TUNTO", navdata.Point{Lat: -33.0, Lon: 20.0, Kind: "fix"}) // a namesake far away
	plan := `{"fields":{"plan":{"callsign":"UPS58","from":"PHNL","to":"VHHH","fixes":[
		{"name":"LILIA","lat":21.0,"lon":-158.0,"has_pos":true},{"name":"TUNTO","via":"Q13"},{"name":"NOWHERE"}]}}}`
	for i, at := range []time.Time{now.Add(-3 * time.Hour), now.Add(-2 * time.Hour)} {
		st.AddMessage(store.Message{ID: int64(i + 1), Hex: "A762CC", T: at, Label: "H1", Text: "FPN/x", Kind: "flight_plan", Decoded: plan, Callsign: "UPS58"})
	}
	st.AddMessage(store.Message{ID: 9, Hex: "A762CC", T: now.Add(-time.Hour), Label: "H1", Text: "other", Kind: "unknown"})
	ts := httptest.NewServer((&Server{Store: st, Nav: nav, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/plan/a762cc", "")
	var plans []struct {
		ID       int64    `json:"id"`
		T        string   `json:"t"`
		Callsign string   `json:"callsign"`
		From     string   `json:"from"`
		To       string   `json:"to"`
		FromName string   `json:"from_name"`
		Missing  []string `json:"missing"`
		Points   []struct {
			Name string  `json:"name"`
			Lat  float64 `json:"lat"`
			Lon  float64 `json:"lon"`
			Via  string  `json:"via"`
			Kind string  `json:"kind"`
		} `json:"points"`
	}
	if err := json.Unmarshal([]byte(body), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("%v: %s", err, body)
	}
	p := plans[0]
	if p.ID != 2 || p.T != "2026-10-30T20:00:00Z" || p.Callsign != "UPS58" || p.From != "PHNL" || p.FromName == "" {
		t.Errorf("identical plans reported once, at the newest: %+v", p)
	}
	var got []string
	for _, pt := range p.Points {
		got = append(got, pt.Name+"/"+pt.Kind)
	}
	if !reflect.DeepEqual(got, []string{"PHNL/airport", "LILIA/given", "TUNTO/fix", "VHHH/airport"}) || !reflect.DeepEqual(p.Missing, []string{"NOWHERE"}) {
		t.Fatalf("points %v missing %v", got, p.Missing)
	}
	if tu := p.Points[2]; tu.Lat != 25 || tu.Via != "Q13" {
		t.Errorf("TUNTO should be the one near the route: %+v", tu)
	}

	// the plan names the origin and destination on the watch
	e, _ := watchlist.Parse("hex:A762CC", now, time.Time{})
	st.AddWatch(e)
	_, body = do(t, "GET", ts.URL+"/api/watches", "")
	var ws []struct {
		Route *struct {
			From, To, Source string
		} `json:"route"`
	}
	json.Unmarshal([]byte(body), &ws)
	if len(ws) != 1 || ws[0].Route == nil || ws[0].Route.From != "PHNL" || ws[0].Route.To != "VHHH" || ws[0].Route.Source != "flight plan" {
		t.Errorf("route = %+v (%s)", ws, body)
	}
}

// A station may hear only part of a message: its copy ends with the last fix cut short (a name, no coordinates).
// When the complete copy of the same plan is heard too, the plan shown is the complete one, whichever came first.
func TestTruncatedPlanCopyDoesNotWin(t *testing.T) {
	for name, order := range map[string][]int{"truncated first": {0, 1}, "complete first": {1, 0}} {
		st, _ := store.Open(":memory:")
		now := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
		copies := []string{
			`{"fields":{"plan":{"callsign":"UPS58","from":"PHNL","to":"VHHH","fixes":[{"name":"LILIA","lat":21,"lon":-158,"has_pos":true},{"name":"ABBEY"}]}}}`,
			`{"fields":{"plan":{"callsign":"UPS58","from":"PHNL","to":"VHHH","fixes":[{"name":"LILIA","lat":21,"lon":-158,"has_pos":true},{"name":"ABBEY","lat":22.27,"lon":114.92,"has_pos":true}]}}}`,
		}
		for i, c := range order {
			st.AddMessage(store.Message{ID: int64(i + 1), Hex: "A762CC", T: now.Add(-time.Duration(3-i) * time.Hour), Label: "H1", Text: "FPN/x", Kind: "flight_plan", Decoded: copies[c]})
		}
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
		_, body := do(t, "GET", ts.URL+"/api/plan/A762CC", "")
		ts.Close()
		st.Close()
		if strings.Count(body, `"id":`) != 1 || !strings.Contains(body, `"name":"ABBEY","lat":22.27`) || strings.Contains(body, `"missing"`) {
			t.Errorf("%s: %s", name, body)
		}
	}
}

func TestFixesAPI(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t1 := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	st.AddMessage(store.Message{ID: 1, Hex: "C880A8", T: t1, Label: "BA", Text: "a", Kind: "cpdlc"})
	st.AddFix("WEDES", 1, 30.668, -123.124, "adsc-eta")
	st.AddFix("AKOTA", 99, 16.46, 117.21, "adsc-chain")
	nav := navdata.New()
	nav.Add("WEDES", navdata.Point{Lat: 30.7, Lon: -123.1, Kind: "fix"}) // the same fix, 2 nm off
	nav.Add("WEDGE", navdata.Point{Lat: 10, Lon: 10, Kind: "fix"})
	nav.Add("OTHER", navdata.Point{Lat: 1, Lon: 1, Kind: "vor"})
	ts := httptest.NewServer((&Server{Store: st, Nav: nav}).Handler())
	defer ts.Close()
	get := func(q string) (r struct {
		Names, Positions, DBNames int
		Fixes                     []struct {
			Name, Kind string
			N          int
			First      *time.Time
			DBNM       *float64 `json:"db_nm"`
		}
	}) {
		_, body := do(t, "GET", ts.URL+"/api/fixes"+q, "")
		var raw struct {
			LearnedNames     int `json:"learned_names"`
			LearnedPositions int `json:"learned_positions"`
			DatabaseNames    int `json:"database_names"`
		}
		json.Unmarshal([]byte(body), &raw)
		json.Unmarshal([]byte(body), &r)
		r.Names, r.Positions, r.DBNames = raw.LearnedNames, raw.LearnedPositions, raw.DatabaseNames
		return
	}

	// No query: everything learned, by name, and nothing from the database.
	r := get("")
	if r.Names != 2 || r.Positions != 2 || r.DBNames != 3 || len(r.Fixes) != 2 || r.Fixes[0].Name != "AKOTA" || r.Fixes[1].Name != "WEDES" {
		t.Fatalf("all = %+v", r)
	}
	if w := r.Fixes[1]; w.Kind != "learned" || w.N != 1 || w.First == nil || !w.First.Equal(t1) || w.DBNM == nil || *w.DBNM > 3 {
		t.Errorf("WEDES = %+v", w)
	}
	if a := r.Fixes[0]; a.DBNM != nil || a.First != nil { // not in the database; its message is not stored
		t.Errorf("AKOTA = %+v", a)
	}

	// A query: learned names containing it, database names starting with it. The database's copy of a
	// learned fix is not listed again.
	r = get("?q=wed")
	if len(r.Fixes) != 2 || r.Fixes[0].Name != "WEDES" || r.Fixes[0].Kind != "learned" || r.Fixes[1].Name != "WEDGE" || r.Fixes[1].Kind != "fix" {
		t.Errorf("q=wed = %+v", r.Fixes)
	}
	// One letter is too short to search the database, but still filters what we learned.
	if r = get("?q=o"); len(r.Fixes) != 1 || r.Fixes[0].Name != "AKOTA" {
		t.Errorf("q=o = %+v", r.Fixes)
	}
	// Without a database the learned fixes are still served.
	ts2 := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts2.Close()
	if code, body := do(t, "GET", ts2.URL+"/api/fixes?q=wed", ""); code != 200 || !strings.Contains(body, `"WEDES"`) {
		t.Errorf("no database: %d %s", code, body)
	}
}

// JA826J: its last ADS-B fix was 400 ft and 1.9 nm from Narita, then nothing. The watch says it landed there, and
// where it was going comes from the airline's own position reports.
func TestLandingAndTheRouteFromPositionReports(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	fix := time.Date(2026, 10, 31, 3, 25, 59, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	st.AddPosition(store.Position{Hex: "86D646", T: fix.Add(-2 * time.Minute), Lat: 35.6455, Lon: 140.4613, AltFt: f(2400), GS: f(181), Track: f(329), VRate: f(-992), Source: "adsb.fi"})
	st.AddPosition(store.Position{Hex: "86D646", T: fix, Lat: 35.738, Lon: 140.3946, AltFt: f(400), GS: f(147), Track: f(330), VRate: f(-1024), Source: "adsb.lol"})
	// A label-83 report four hours earlier names the destination.
	rep := fix.Add(-4 * time.Hour)
	rlat, rlon := 28.71, 173.77
	st.AddMessage(store.Message{ID: 1, Hex: "86D646", T: rep, Label: "83", Text: "x", Kind: "aoc_position", Lat: &rlat, Lon: &rlon, PosT: &rep,
		Decoded: `{"fields":{"origin":"PHNL","dest":"RJAA"}}`})
	e, _ := watchlist.Parse("tail:JA826J", fix.Add(-time.Hour), time.Time{})
	e.Hex = "86D646"
	st.AddWatch(e)

	get := func(now time.Time) (ws []struct {
		Route   *struct{ From, To, Source string }
		Landing *struct {
			Code   string
			DistNM float64 `json:"dist_nm"`
			At     time.Time
		}
	}) {
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
		defer ts.Close()
		_, body := do(t, "GET", ts.URL+"/api/watches", "")
		if err := json.Unmarshal([]byte(body), &ws); err != nil || len(ws) != 1 {
			t.Fatalf("%v: %s", err, body)
		}
		return
	}
	// Half a minute after the fix it is still on the way down.
	if ws := get(fix.Add(30 * time.Second)); ws[0].Landing != nil {
		t.Errorf("landed too soon: %+v", ws[0].Landing)
	}
	ws := get(fix.Add(10 * time.Minute))
	if ws[0].Landing == nil || ws[0].Landing.Code != "RJAA" || ws[0].Landing.DistNM > 3 || ws[0].Landing.At.Sub(fix) > time.Minute {
		t.Errorf("landing = %+v", ws[0].Landing)
	}
	if ws[0].Route == nil || ws[0].Route.To != "RJAA" || ws[0].Route.From != "PHNL" || ws[0].Route.Source != "airline report" {
		t.Errorf("route = %+v", ws[0].Route)
	}
	// A later report with a position, from any source, means it did not land there.
	later := fix.Add(5 * time.Minute)
	llat, llon := 35.9, 140.2
	st.AddMessage(store.Message{ID: 2, Hex: "86D646", T: later, Label: "B6", Text: "y", Kind: "adsc", Lat: &llat, Lon: &llon, PosT: &later})
	if ws := get(fix.Add(10 * time.Minute)); ws[0].Landing != nil {
		t.Errorf("a newer report ignored: %+v", ws[0].Landing)
	}
}

func TestAirportsAPI(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	fix := time.Date(2026, 10, 31, 3, 25, 59, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	st.AddPosition(store.Position{Hex: "86D646", T: fix, Lat: 35.738, Lon: 140.3946, AltFt: f(400), GS: f(147), Track: f(330), VRate: f(-1024), Source: "adsb.lol"})
	rep := fix.Add(-4 * time.Hour)
	rlat, rlon := 28.71, 173.77
	st.AddMessage(store.Message{ID: 1, Hex: "86D646", T: rep, Label: "83", Text: "x", Kind: "aoc_position", Lat: &rlat, Lon: &rlon, PosT: &rep,
		Decoded: `{"fields":{"origin":"PHNL","dest":"RJAA"}}`})
	e, _ := watchlist.Parse("tail:JA826J", fix.Add(-time.Hour), time.Time{})
	e.Hex = "86D646"
	st.AddWatch(e)
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return fix.Add(10 * time.Minute) }}).Handler())
	defer ts.Close()

	type resp struct {
		Total    int
		Watched  bool
		More     bool
		Airports []struct {
			ICAO, IATA, Name string
			ElevFt           *float64 `json:"elev_ft"`
			Why              []string
		}
	}
	get := func(q string) (r resp) {
		_, body := do(t, "GET", ts.URL+"/api/airports"+q, "")
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return
	}
	// No query: the airports the watches use, with the reasons.
	r := get("")
	if !r.Watched || r.Total < 8000 || len(r.Airports) != 2 || r.Airports[0].ICAO != "PHNL" || r.Airports[1].ICAO != "RJAA" {
		t.Fatalf("watched = %+v", r)
	}
	if got := strings.Join(r.Airports[1].Why, "; "); got != "JA826J route destination (airline report); JA826J landed" {
		t.Errorf("RJAA why = %q", got)
	}
	// A query: ranked matches, with elevation, and the reasons on the ones the watches use.
	r = get("?q=nrt")
	if r.Watched || len(r.Airports) == 0 || r.Airports[0].ICAO != "RJAA" || r.Airports[0].ElevFt == nil || *r.Airports[0].ElevFt != 141 || len(r.Airports[0].Why) != 2 {
		t.Errorf("q=nrt = %+v", r.Airports)
	}
	if r = get("?q=international"); len(r.Airports) != 50 || !r.More {
		t.Errorf("limit: %d more %v", len(r.Airports), r.More)
	}
	if r = get("?q=zzzzqq"); len(r.Airports) != 0 || r.Watched {
		t.Errorf("no match = %+v", r)
	}
}

// A fix the 2013 database lacks, but whose position we have learned from the aircraft's own reports, is placed from
// that, and says so; where both have it, what we learned wins.
func TestPlanUsesLearnedFixes(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC)
	nav := navdata.New()
	nav.Add("ITIDE", navdata.Point{Lat: -26.735, Lon: 153.49, Kind: "fix"})
	nav.Add("GOMIK", navdata.Point{Lat: -9.60, Lon: -172.90, Kind: "fix"}) // 2013 position, a few nm from the learned one
	plan := `{"fields":{"plan":{"callsign":"CPA198","from":"NZAA","to":"VHHH","fixes":[
		{"name":"ITIDE"},{"name":"ELNIT"},{"name":"GOMIK"},{"name":"UNKNOWN"}]}}}`
	st.AddMessage(store.Message{ID: 1, Hex: "780AA4", T: now.Add(-4 * time.Hour), Label: "H1", Text: "FPN/x", Kind: "flight_plan", Decoded: plan})
	st.AddFix("ELNIT", 50, -20.0, 148.0, "adsc-eta")
	st.AddFix("GOMIK", 51, -9.464, -172.938, "adsc-eta")
	ts := httptest.NewServer((&Server{Store: st, Nav: nav, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/plan/780aa4", "")
	var plans []struct {
		Points []struct {
			Name, Kind string
			Lat, Lon   float64
		}
		Missing []string
	}
	if err := json.Unmarshal([]byte(body), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("%v: %s", err, body)
	}
	kinds := map[string]string{}
	pos := map[string][2]float64{}
	for _, p := range plans[0].Points {
		kinds[p.Name], pos[p.Name] = p.Kind, [2]float64{p.Lat, p.Lon}
	}
	if kinds["ITIDE"] != "fix" || kinds["ELNIT"] != "learned" || kinds["GOMIK"] != "learned" {
		t.Errorf("kinds = %v", kinds)
	}
	if pos["ELNIT"] != [2]float64{-20.0, 148.0} || pos["GOMIK"] != [2]float64{-9.464, -172.938} {
		t.Errorf("positions = %v", pos)
	}
	if len(plans[0].Missing) != 1 || plans[0].Missing[0] != "UNKNOWN" {
		t.Errorf("missing = %v", plans[0].Missing)
	}
}

func TestPauseAndResumeAPI(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("tail:JA826J", now.Add(-time.Hour), time.Time{})
	e.Hex, e.Status = "86D646", watchlist.StatusTracking
	e, _ = st.AddWatch(e)
	f := func(v float64) *float64 { return &v }
	st.AddPosition(store.Position{Hex: "86D646", T: now.Add(-time.Hour), Lat: 35.7, Lon: 140.4, AltFt: f(400), GS: f(147), Track: f(330), Source: "adsb.lol"})
	lat, lon := 28.7, 173.8
	st.AddMessage(store.Message{ID: 1, Hex: "86D646", T: now.Add(-5 * time.Hour), Label: "83", Text: "x", Kind: "aoc_position", Lat: &lat, Lon: &lon, PosT: &now})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	url := fmt.Sprintf("%s/api/watches/%d", ts.URL, e.ID)

	code, body := do(t, "PATCH", url, `{"paused":true}`)
	if code != 200 || !strings.Contains(body, `"status":"paused"`) {
		t.Fatalf("pause: %d %s", code, body)
	}
	// Still in the list, and everything stored about the aircraft is still served.
	_, body = do(t, "GET", ts.URL+"/api/watches", "")
	if !strings.Contains(body, `"status":"paused"`) || !strings.Contains(body, `"ident":"JA826J"`) {
		t.Errorf("list = %s", body)
	}
	if _, body = do(t, "GET", ts.URL+"/api/track/86D646", ""); !strings.Contains(body, `"alt_ft":400`) {
		t.Errorf("track = %s", body)
	}
	if _, body = do(t, "GET", ts.URL+"/api/messages/86D646", ""); !strings.Contains(body, `"kind":"aoc_position"`) {
		t.Errorf("messages = %s", body)
	}
	if code, _ = do(t, "PATCH", url, `{"paused":true}`); code != 409 {
		t.Errorf("pausing twice: %d", code)
	}
	if code, body = do(t, "PATCH", url, `{"paused":false}`); code != 200 || !strings.Contains(body, `"status":"tracking"`) {
		t.Errorf("resume: %d %s", code, body)
	}
	if code, _ = do(t, "PATCH", url, `{"paused":false}`); code != 409 {
		t.Errorf("resuming a running watch: %d", code)
	}
	if code, _ = do(t, "PATCH", url, `{}`); code != 400 {
		t.Errorf("no body: %d", code)
	}
	if code, _ = do(t, "PATCH", ts.URL+"/api/watches/999", `{"paused":true}`); code != 404 {
		t.Errorf("missing: %d", code)
	}
	if code, _ = do(t, "PATCH", ts.URL+"/api/watches/x", `{"paused":true}`); code != 400 {
		t.Errorf("bad id: %d", code)
	}
}

func TestWatchesCarryWhatTheLastPollFound(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 6, 50, 0, 0, time.UTC)
	e, _ := watchlist.Parse("tail:N575UP", now.Add(-time.Hour), time.Time{})
	e, _ = st.AddWatch(e)
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	get := func() (ws []struct {
		Poll *struct {
			Asked   time.Time
			Outcome string
			Detail  string
			Heard   *time.Time
			Source  string
			Misses  int
		}
	}) {
		_, body := do(t, "GET", ts.URL+"/api/watches", "")
		if err := json.Unmarshal([]byte(body), &ws); err != nil || len(ws) != 1 {
			t.Fatalf("%v: %s", err, body)
		}
		return
	}
	// Never asked: nothing to say.
	if ws := get(); ws[0].Poll != nil {
		t.Errorf("poll before any ask: %+v", ws[0].Poll)
	}
	st.RecordPoll(e.ID, now.Add(-5*time.Minute), "heard", "", now.Add(-5*time.Minute), "adsb.fi")
	st.RecordPoll(e.ID, now.Add(-2*time.Minute), "not_seen", "", time.Time{}, "")
	st.RecordPoll(e.ID, now.Add(-time.Minute), "not_seen", "", time.Time{}, "")
	p := get()[0].Poll
	if p == nil || p.Outcome != "not_seen" || p.Misses != 2 || p.Source != "adsb.fi" || p.Heard == nil || !p.Heard.Equal(now.Add(-5*time.Minute)) || !p.Asked.Equal(now.Add(-time.Minute)) {
		t.Errorf("poll = %+v", p)
	}
	// Removed and added again: no memory of the one before.
	st.DeleteWatch(e.ID)
	e2, _ := st.AddWatch(e)
	if ws := get(); ws[0].Poll != nil {
		t.Errorf("a new watch %d inherited %+v", e2.ID, ws[0].Poll)
	}
}
