package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/avanha/fullflight/internal/airframes"
	"github.com/avanha/fullflight/internal/history"
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

// The airframes page shows each aircraft's last flight: the number and callsign, the route and when it left.
func TestAirframesLastFlight(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	st.RecordAirframe(store.Airframe{Hex: "A762CC", Tail: "N575UP", Type: "B744", Source: "airframes.io", LastSeen: now.Add(-time.Hour)})
	st.RecordAirframe(store.Airframe{Hex: "780AB8", Tail: "B-LRM", Type: "A359", Source: "airframes.io", LastSeen: now.Add(-time.Hour)})
	for i := 0; i < 4; i++ { // ADS-B with the callsign, leaving the ground at 18:02
		st.AddPosition(store.Position{Hex: "A762CC", T: now.Add(-4*time.Hour + time.Duration(i)*2*time.Minute), Lat: 61 + float64(i)/10, Lon: -150, OnGround: i == 0, Callsign: "UPS65", Source: "adsb.lol"})
	}
	pt := now.Add(-4*time.Hour + 3*time.Minute)
	st.AddMessage(store.Message{ID: 1, Hex: "A762CC", T: pt, PosT: &pt, Kind: "miam", Label: "MA", Text: "x", Decoded: `{"fields":{"a350":{"Origin":"VHHH","Dest":"PANC"}}}`})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/airframes", "")
	var got []airframeJSON
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	by := map[string]airframeJSON{}
	for _, a := range got {
		by[a.Hex] = a
	}
	lf := by["A762CC"].LastFlight
	if lf == nil || lf.Flight != "5X65" || lf.Callsign != "UPS65" || lf.From != "VHHH" || lf.To != "PANC" || lf.DepartedHow != "adsb" ||
		!lf.Departed.Equal(now.Add(-4*time.Hour+2*time.Minute)) || lf.ToName == "" {
		t.Errorf("last flight = %+v", lf)
	}
	if by["780AB8"].LastFlight != nil {
		t.Errorf("an aircraft with no flight shows none: %+v", by["780AB8"].LastFlight)
	}
}

// An aircraft known only by its hex still gets its registration where the address gives it away.
func TestAirframesTailFromHex(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, h := range []string{"A762CC", "C023C8", "780AB8"} { // known only from a watch, so with no registration on file
		e, _ := watchlist.Parse("hex:"+h, now, time.Time{})
		e.Tail = ""
		if _, err := st.AddWatch(e); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/airframes", "")
	var got []airframeJSON
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	tails := map[string]string{}
	for _, a := range got {
		tails[a.Hex] = a.Tail
	}
	if tails["A762CC"] != "N575UP" || tails["C023C8"] != "C-FNOH" || tails["780AB8"] != "" {
		t.Errorf("tails = %v", tails)
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
	st.AddMessage(store.Message{ID: 5, Hex: "A48E3A", T: now.Add(-time.Hour), Label: "7B", Text: "x5", Kind: "text_message", Summary: "Crew message: CX85 EASTERN GREENLAND ROUTING"}) // readable text is listed, not hidden
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
			for k := 0; k < 4; k++ { // the aircraft is heard around the report, so it belongs to a flight
				st.AddPosition(store.Position{Hex: "780AB8", T: at.Add(time.Duration(k-2) * time.Minute), Lat: 10 + float64(i) + float64(k)/10, Lon: 100, Source: "adsb.lol"})
			}
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

// acrossThePacific gives an aircraft the positions of the flight that ends at fix: one every ten minutes for the four hours before, so
// the reports sent on the way belong to a flight.
func acrossThePacific(st *store.Store, hex string, fix time.Time) {
	for i := 0; i < 24; i++ {
		st.AddPosition(store.Position{Hex: hex, T: fix.Add(-4*time.Hour + time.Duration(i)*10*time.Minute), Lat: 28.7 + float64(i)*0.3, Lon: 173.8 - float64(i)*1.4, Source: "adsb.fi"})
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
	acrossThePacific(st, "86D646", fix)
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

// The sidebar's route and flight are those of the aircraft's newest flight, the same flights the map lists: an origin no report gave
// but ADS-B saw is shown as seen, the next flight a message announces shows before it has a position, and the route of the flight
// before is not carried over to the next.
func TestSidebarReadsTheNewestFlight(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	f := func(v float64) *float64 { return &v }
	now := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
	e, _ := watchlist.Parse("tail:N77019", now.Add(-12*time.Hour), time.Time{})
	e.Hex = "AA6BA4"
	st.AddWatch(e)
	get := func(at time.Time) (w struct {
		Route *struct {
			From, To, Source string
			FromName         string `json:"from_name"`
			FromSeen         bool   `json:"from_seen"`
			Stale            bool
		}
		Flight *struct{ Callsign, Number, Source string }
	}) {
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return at }}).Handler())
		defer ts.Close()
		_, body := do(t, "GET", ts.URL+"/api/watches", "")
		var ws []struct {
			Route *struct {
				From, To, Source string
				FromName         string `json:"from_name"`
				FromSeen         bool   `json:"from_seen"`
				Stale            bool
			}
			Flight *struct{ Callsign, Number, Source string }
		}
		if err := json.Unmarshal([]byte(body), &ws); err != nil || len(ws) != 1 {
			t.Fatalf("%v: %s", err, body)
		}
		return ws[0]
	}

	// seen taking off from Los Angeles, and no message gave a route
	t0 := now.Add(-3 * time.Hour)
	for i := 0; i < 3; i++ {
		st.AddPosition(store.Position{Hex: "AA6BA4", T: t0.Add(time.Duration(i) * time.Minute), Lat: 33.9402 + float64(i)*1e-4, Lon: -118.4118, OnGround: true, GS: f(0), Callsign: "TZP23", Source: "adsb.lol"})
	}
	for i, alt := range []float64{1500, 5000, 12000, 20000, 30000} {
		st.AddPosition(store.Position{Hex: "AA6BA4", T: t0.Add(10*time.Minute + time.Duration(i)*5*time.Minute), Lat: 34.2 + float64(i)*0.3, Lon: -119 - float64(i)*0.4, AltFt: f(alt), Callsign: "TZP23", Source: "adsb.lol"})
	}
	w := get(now)
	if w.Route == nil || w.Route.From != "KLAX" || !w.Route.FromSeen || w.Route.To != "" || w.Route.Source != "seen on ADS-B" || w.Route.FromName == "" {
		t.Errorf("an origin seen on ADS-B: %+v", w.Route)
	}
	if w.Flight == nil || w.Flight.Callsign != "TZP23" || w.Flight.Source != "ADS-B" {
		t.Errorf("the flight it is on: %+v", w.Flight)
	}

	// then the airline announces the next flight, due out at 23:30, before the aircraft has a position under it
	sched := now.Add(90 * time.Minute)
	st.AddMessage(store.Message{ID: 1, Hex: "AA6BA4", T: now.Add(-10 * time.Minute), Label: "RF", Text: "a", Kind: "ops_report", Flight: "UA888", Callsign: "UAL888",
		Decoded: `{"fields":{"ops":{"event":"FDACOM","origin":"KLAX","dest":"RJAA","sched":"` + sched.Format(time.RFC3339) + `"}}}`})
	if w = get(now); w.Route == nil || w.Route.From != "KLAX" || !w.Route.FromSeen {
		t.Errorf("a flight not due yet is not the newest: %+v", w.Route)
	}
	w = get(sched.Add(time.Minute))
	if w.Route == nil || w.Route.From != "KLAX" || w.Route.To != "RJAA" || w.Route.FromSeen || w.Route.Source != "airline report" || w.Flight == nil || w.Flight.Number != "UA888" || w.Flight.Source != "messages" {
		t.Errorf("the next flight, announced and due: route %+v flight %+v", w.Route, w.Flight)
	}

	// once an aircraft is on a new flight with no route reported, the flight before's route is not shown for it
	e2, _ := watchlist.Parse("tail:N24993", now.Add(-30*time.Hour), time.Time{})
	e2.Hex = "A2551D"
	st.AddWatch(e2)
	for i := 0; i < 6; i++ {
		at := now.Add(-20*time.Hour + time.Duration(i)*20*time.Minute)
		st.AddPosition(store.Position{Hex: "A2551D", T: at, Lat: 40 + float64(i)/10, Lon: -100, AltFt: f(35000), Callsign: "UAL100", Source: "adsb.lol"})
	}
	rep := now.Add(-20*time.Hour + 20*time.Minute)
	st.AddMessage(store.Message{ID: 2, Hex: "A2551D", T: rep, PosT: &rep, Label: "MA", Text: "b", Kind: "miam", Decoded: `{"fields":{"rep081":{"Origin":"KDEN","Dest":"EDDF"}}}`})
	for i := 0; i < 4; i++ {
		st.AddPosition(store.Position{Hex: "A2551D", T: now.Add(-2*time.Hour + time.Duration(i)*5*time.Minute), Lat: 41 + float64(i)/10, Lon: -101, AltFt: f(5000), Callsign: "UAL200", Source: "adsb.lol"})
	}
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/watches", "")
	var ws []struct {
		Hex   string
		Route *struct{ From, To string }
	}
	if err := json.Unmarshal([]byte(body), &ws); err != nil || len(ws) != 2 {
		t.Fatalf("%v: %s", err, body)
	}
	for _, w := range ws {
		if w.Hex == "A2551D" && w.Route != nil {
			t.Errorf("UAL200 does not take the route of UAL100: %+v", w.Route)
		}
	}
}

// An aircraft parked at its destination is "landed" from when it touched down, not from its newest fix on the ground; and once it
// takes the callsign of its next flight it is that flight, not yet departed.
func TestAParkedAircraftTakesItsNextFlight(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	f := func(v float64) *float64 { return &v }
	land := time.Date(2026, 11, 1, 20, 53, 6, 0, time.UTC)
	air := store.Position{Hex: "A2551D", T: land.Add(-3 * time.Minute), Lat: 39.89, Lon: -104.5, AltFt: f(7000), GS: f(160), Track: f(270), Callsign: "UAL181", Source: "adsb.lol"}
	st.AddPosition(air)
	n := 0
	gate := func(at time.Time, cs string) {
		n++ // a position at the same place as the last is not stored again
		st.AddPosition(store.Position{Hex: "A2551D", T: at, Lat: 39.8554 + float64(n)*1e-5, Lon: -104.6806, OnGround: true, GS: f(0), Callsign: cs, Source: "adsb.lol"})
	}
	gate(land, "UAL181")
	gate(land.Add(10*time.Minute), "UAL181")
	e, _ := watchlist.Parse("tail:N24993", land.Add(-time.Hour), time.Time{})
	e.Hex = "A2551D"
	st.AddWatch(e)
	get := func(now time.Time) *landingJSON {
		ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
		defer ts.Close()
		_, body := do(t, "GET", ts.URL+"/api/watches", "")
		var ws []struct{ Landing *landingJSON }
		if err := json.Unmarshal([]byte(body), &ws); err != nil || len(ws) != 1 {
			t.Fatalf("%v: %s", err, body)
		}
		return ws[0].Landing
	}
	l := get(land.Add(time.Hour))
	if l == nil || l.Code != "KDEN" || !l.At.Equal(land) || l.Next != "" {
		t.Fatalf("still the flight that landed: %+v", l)
	}
	// the airline names another flight before the aircraft's callsign changes: that is the next flight too
	st.AddMessage(store.Message{ID: 90, Hex: "A2551D", T: land.Add(2 * time.Hour), Label: "RF", Text: "next", Kind: "ops_report", Flight: "UA300", Callsign: "UAL300",
		Decoded: `{"fields":{"ops":{"event":"FDACOM"}}}`})
	if l = get(land.Add(2*time.Hour + time.Minute)); l == nil || l.Next != "UAL300" || !l.At.Equal(land) {
		t.Errorf("the next flight, named by a message: %+v", l)
	}
	gate(land.Add(3*time.Hour+30*time.Minute), "UAL177")
	gate(land.Add(3*time.Hour+40*time.Minute), "UAL177")
	l = get(land.Add(4 * time.Hour))
	if l == nil || l.Next != "UAL177" || !l.At.Equal(land) {
		t.Errorf("the next flight, not departed, with the landing time unchanged: %+v", l)
	}
}

// The explain endpoint gives the same flights as the flights endpoint, with the reasoning for each.
func TestExplainFlights(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		st.AddPosition(store.Position{Hex: "A2551D", T: now.Add(-3*time.Hour + time.Duration(i)*10*time.Minute), Lat: 40 + float64(i)/10, Lon: -100, Callsign: "UAL100", Source: "x"})
	}
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, plain := do(t, "GET", ts.URL+"/api/flights/A2551D", "")
	_, body := do(t, "GET", ts.URL+"/api/flights/A2551D/explain", "")
	var flightsOnly []struct{ Id, Flight string }
	var ex struct {
		Flights []struct {
			Id, Flight string
			Why        []struct{ Stage, Rule, Detail string }
		}
	}
	if err := json.Unmarshal([]byte(plain), &flightsOnly); err != nil || len(flightsOnly) != 1 {
		t.Fatalf("%v: %s", err, plain)
	}
	if err := json.Unmarshal([]byte(body), &ex); err != nil || len(ex.Flights) != 1 || ex.Flights[0].Id != flightsOnly[0].Id {
		t.Fatalf("%v: %s", err, body)
	}
	if w := ex.Flights[0].Why; len(w) == 0 || w[0].Rule != "run" || !strings.Contains(w[0].Detail, "6 position(s) attached") {
		t.Errorf("why: %+v", w)
	}
	if strings.Contains(plain, `"why"`) {
		t.Error("the usual list carries no reasoning")
	}
}

func TestAirportsAPI(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	fix := time.Date(2026, 10, 31, 3, 25, 59, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	st.AddPosition(store.Position{Hex: "86D646", T: fix, Lat: 35.738, Lon: 140.3946, AltFt: f(400), GS: f(147), Track: f(330), VRate: f(-1024), Source: "adsb.lol"})
	acrossThePacific(st, "86D646", fix)
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

// A route typed in for a flight with none: it is checked, applied to that flight only, shown as entered by hand,
// and replaced by the messages' own route if one turns up.
func TestSetFlightRoute(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ { // one flight, UPS65, with positions and no route anywhere
		st.AddPosition(store.Position{Hex: "A762CC", T: now.Add(-5*time.Hour + time.Duration(i)*10*time.Minute), Lat: 61 + float64(i)/10, Lon: -150, Callsign: "UPS65", Source: "adsb.lol"})
	}
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	inFlight := now.Add(-4*time.Hour - 30*time.Minute).Unix()
	put := func(hex string, start int64, from, to string) (int, string) {
		return do(t, "PUT", ts.URL+"/api/flights/"+hex+"/route", fmt.Sprintf(`{"start":%d,"from":%q,"to":%q}`, start, from, to))
	}
	flightsNow := func() flightSegJSON {
		_, body := do(t, "GET", ts.URL+"/api/flights/A762CC?hours=96", "")
		var segs []flightSegJSON
		if err := json.Unmarshal([]byte(body), &segs); err != nil || len(segs) != 1 {
			t.Fatalf("flights = %s (%v)", body, err)
		}
		return segs[0]
	}
	if sg := flightsNow(); sg.From != "" || sg.To != "" {
		t.Fatalf("setup: %+v", sg)
	}

	code, body := put("A762CC", inFlight, "hkg", "anc") // IATA, any case
	var sg flightSegJSON
	if code != 200 || json.Unmarshal([]byte(body), &sg) != nil || sg.From != "VHHH" || sg.To != "PANC" || !sg.FromManual || !sg.ToManual || sg.FromName == "" || sg.ToName == "" {
		t.Fatalf("PUT = %d %s", code, body)
	}
	if got := flightsNow(); got.From != "VHHH" || got.To != "PANC" || !got.FromManual {
		t.Errorf("GET after PUT: %+v", got)
	}
	for name, c := range map[string]struct {
		hex      string
		start    int64
		from, to string
		want     int
	}{
		"too long":               {"A762CC", inFlight, "HKGGG", "ANC", 400},
		"three letters, unknown": {"A762CC", inFlight, "ZZZ", "ANC", 400},
		"same airport":           {"A762CC", inFlight, "ANC", "PANC", 400},
		"no flight then":         {"A762CC", now.Add(-48 * time.Hour).Unix(), "HKG", "ANC", 404},
		"no such aircraft":       {"780AB8", inFlight, "HKG", "ANC", 404},
		"not a code":             {"A762CC", inFlight, "H!G", "ANC", 400},
		"not a hex":              {"zzz", inFlight, "HKG", "ANC", 400},
	} {
		if code, body := put(c.hex, c.start, c.from, c.to); code != c.want {
			t.Errorf("%s: %d %s, want %d", name, code, body, c.want)
		}
	}
	if got := flightsNow(); got.From != "VHHH" || got.To != "PANC" {
		t.Errorf("a refused entry changed the flight: %+v", got)
	}

	// A small field the list lacks, as four characters, is kept as typed; one end alone is fine.
	if code, body := put("A762CC", inFlight, "", "PA9X"); code != 200 {
		t.Fatalf("PUT = %d %s", code, body)
	}
	if got := flightsNow(); got.From != "" || got.To != "PA9X" || got.FromManual || !got.ToManual {
		t.Errorf("one end only: %+v", got)
	}

	// The messages' own route wins.
	pt := now.Add(-4 * time.Hour)
	st.AddMessage(store.Message{ID: 7, Hex: "A762CC", T: pt, PosT: &pt, Kind: "miam", Label: "MA", Text: "x", Decoded: `{"fields":{"a350":{"Origin":"VHHH","Dest":"PANC"}}}`})
	if got := flightsNow(); got.From != "VHHH" || got.To != "PANC" || got.FromManual || got.ToManual {
		t.Errorf("data should win: %+v", got)
	}
}

// A D-ATIS request is placed at the airport it asks about, so the map can show where the crew is looking.
func TestMessagesDATISAirport(t *testing.T) {
	st, _ := store.Open(":memory:")
	now := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	st.AddMessage(store.Message{ID: 1, Hex: "C023C8", T: now.Add(-time.Hour), Label: "B9", Text: "a", Kind: "datis_request", Summary: "D-ATIS arrival request for RJBB"})
	st.AddMessage(store.Message{ID: 2, Hex: "C023C8", T: now.Add(-50 * time.Minute), Label: "B9", Text: "b", Kind: "datis_request", Summary: "D-ATIS departure request for ZZZZ"})
	st.AddMessage(store.Message{ID: 3, Hex: "C023C8", T: now.Add(-40 * time.Minute), Label: "SA", Text: "c", Kind: "media_advisory", Summary: "Link"})
	st.AddMessage(store.Message{ID: 4, Hex: "C023C8", T: now.Add(-30 * time.Minute), Label: "5U", Text: "d", Kind: "ops_report", Summary: "D-ATIS request for YSSY, YMML"})
	st.AddMessage(store.Message{ID: 5, Hex: "C023C8", T: now.Add(-20 * time.Minute), Label: "A9", Text: "e", Kind: "weather_report", Summary: "D-ATIS arrival information P for PHNL: 060°/8 kt, 10 SM"})
	st.AddMessage(store.Message{ID: 6, Hex: "C023C8", T: now.Add(-10 * time.Minute), Label: "5U", Text: "f", Kind: "ops_report", Summary: "Weather request (METAR) for YPPH, YSSY"})
	st.AddMessage(store.Message{ID: 7, Hex: "C023C8", T: now.Add(-5 * time.Minute), Label: "5U", Text: "g", Kind: "ops_report", Summary: "Weather request (TAF) for YSSY"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/messages/C023C8", "")
	var ms []struct {
		ID       int64 `json:"id"`
		Airports []struct {
			Code, IATA, Type, Wx string
			Lat, Lon             float64
		} `json:"airports"`
	}
	if err := json.Unmarshal([]byte(body), &ms); err != nil || len(ms) != 7 {
		t.Fatalf("%v: %s", err, body)
	}
	if len(ms[0].Airports) != 1 || ms[0].Airports[0].Code != "RJBB" || ms[0].Airports[0].IATA != "KIX" || ms[0].Airports[0].Type != "arrival" || ms[0].Airports[0].Lat < 34 || ms[0].Airports[0].Lat > 35 {
		t.Errorf("RJBB request: %+v", ms[0].Airports)
	}
	if len(ms[1].Airports) != 0 || len(ms[2].Airports) != 0 {
		t.Errorf("an unknown airport and a non-ATIS message get none: %+v %+v", ms[1].Airports, ms[2].Airports)
	}
	// Qantas asks for two at once, and a broadcast sent back names its airport too
	if len(ms[3].Airports) != 2 || ms[3].Airports[1].Code != "YMML" || len(ms[4].Airports) != 1 || ms[4].Airports[0].Code != "PHNL" {
		t.Errorf("two airports, and a reply: %+v %+v", ms[3].Airports, ms[4].Airports)
	}
	// METAR and TAF requests are marked the same way and say what they are
	if len(ms[5].Airports) != 2 || ms[5].Airports[0].Code != "YPPH" || ms[5].Airports[1].Code != "YSSY" || ms[5].Airports[0].Wx != "METAR" || ms[3].Airports[0].Wx != "" || len(ms[6].Airports) != 1 || ms[6].Airports[0].Code != "YSSY" || ms[6].Airports[0].Wx != "TAF" {
		t.Errorf("a METAR request: %+v, a TAF request: %+v", ms[5].Airports, ms[6].Airports)
	}
}

// A watch started from the airframes page is by registration, with the hex the page already has: a registration whose
// address does not follow from it would otherwise sit unresolved until a lookup found it.
func TestTailWatchWithKnownHex(t *testing.T) {
	st, _ := store.Open(":memory:")
	ts := httptest.NewServer((&Server{Store: st, Now: time.Now}).Handler())
	defer ts.Close()
	code, body := do(t, "POST", ts.URL+"/api/watches", `{"input":"tail:B-LRM","hex":"780ab8"}`)
	if code != 201 || !strings.Contains(body, `"kind":"tail"`) || !strings.Contains(body, `"hex":"780AB8"`) {
		t.Errorf("tail with hex: %d %s", code, body)
	}
	// a US registration keeps the address that follows from it, whatever was sent; a bad hex is ignored
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"tail:N575UP","hex":"123456"}`)
	if code != 201 || strings.Contains(body, "123456") {
		t.Errorf("US tail: %d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/api/watches", `{"input":"tail:B-LRD","hex":"nonsense"}`)
	if code != 201 || strings.Contains(body, "nonsense") {
		t.Errorf("bad hex: %d %s", code, body)
	}
}

// The blocks of a message and its shorter copies are listed under the whole message, not as rows of their own.
func TestMessagesFoldTheBlocksUnderTheWholeMessage(t *testing.T) {
	st, _ := store.Open(":memory:")
	now := time.Date(2026, 10, 23, 21, 30, 0, 0, time.UTC)
	first := "<701>IEG\r\n N24993UAL182    ER  72211026232013KDENEDDF"
	rest := "021 641\r\n 958959  92261058 8031"
	add := func(id int64, at time.Duration, num, text string) {
		st.AddMessage(store.Message{ID: id, Hex: "A2551D", T: now.Add(at), Label: "H1", MsgNum: num, Text: text, Kind: "unknown"})
	}
	add(1, 0, "D94", first)                                 // the first block
	add(2, 4*time.Second, "D94", rest)                      // the second
	add(3, 8*time.Second, "D94", first+"\r\n"+rest)         // the whole, joined up
	add(4, 9*time.Second, "D95", "something else entirely") // another message
	add(5, 10*time.Second, "D94", "short")                  // too short to say anything
	add(6, 20*time.Minute, "D94", first)                    // the number is reused much later: not a part of the first
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now.Add(time.Hour) }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/messages/A2551D", "")
	var ms []struct {
		ID     int64                `json:"id"`
		PartOf int64                `json:"part_of"`
		Parts  []struct{ ID int64 } `json:"parts"`
	}
	if err := json.Unmarshal([]byte(body), &ms); err != nil || len(ms) != 6 {
		t.Fatalf("%v: %s", err, body)
	}
	by := map[int64]int{}
	for i, m := range ms {
		by[m.ID] = i
	}
	for _, id := range []int64{1, 2} {
		if ms[by[id]].PartOf != 3 {
			t.Errorf("message %d is part of %d, want 3", id, ms[by[id]].PartOf)
		}
	}
	if w := ms[by[3]]; w.PartOf != 0 || len(w.Parts) != 2 || w.Parts[0].ID != 1 || w.Parts[1].ID != 2 {
		t.Errorf("the whole: %+v", w)
	}
	for _, id := range []int64{4, 5, 6} {
		if ms[by[id]].PartOf != 0 || len(ms[by[id]].Parts) != 0 {
			t.Errorf("message %d should stand alone: %+v", id, ms[by[id]])
		}
	}
}

// An oceanic clearance is served with the plans but marked as one: only the fixes it gives, resolved to positions (the
// named ones from the navigation database), with no origin or destination point added on.
func TestOceanicClearanceIsServedAsItsOwnRoute(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.UTC)
	nav := navdata.New()
	nav.Add("ATSIX", navdata.Point{Lat: 61.0, Lon: -10.0, Kind: "fix"})
	nav.Add("PIDSO", navdata.Point{Lat: 61.5, Lon: -60.0, Kind: "fix"})
	dec := `{"fields":{"clearance":{"what":"clearance","callsign":"UAL26","dest":"KDEN","track":"NAT B","entry":"ATSIX","entry_time":"1316","level":370,"mach":0.85,
		"route":{"callsign":"UAL26","from":"","to":"","fixes":[{"name":"ATSIX"},{"name":"62N020W","lat":62,"lon":-20,"has_pos":true},{"name":"PIDSO"}]}}}}`
	for i, at := range []time.Time{now.Add(-90 * time.Minute), now.Add(-80 * time.Minute)} { // sent twice
		st.AddMessage(store.Message{ID: int64(i + 1), Hex: "A2551D", T: at, Label: "B2", Text: "x" + string(rune('a'+i)), Kind: "oceanic_clearance", Decoded: dec})
	}
	st.AddMessage(store.Message{ID: 3, Hex: "A2551D", T: now.Add(-100 * time.Minute), Label: "B1", Text: "req", Kind: "oceanic_clearance", Decoded: `{"fields":{"clearance":{"what":"request","callsign":"UAL26"}}}`})
	ts := httptest.NewServer((&Server{Store: st, Nav: nav, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/plan/A2551D", "")
	var plans []struct {
		Kind, Detail, To string
		Points           []struct {
			Name string
			Lat  float64
			Kind string
		}
	}
	if err := json.Unmarshal([]byte(body), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("%v: %s", err, body)
	}
	p := plans[0]
	if p.Kind != "clearance" || p.To != "KDEN" || p.Detail != "NAT B · FL370 M0.85 · entry ATSIX 1316" || len(p.Points) != 3 {
		t.Errorf("clearance: %+v", p)
	}
	if p.Points[0].Name != "ATSIX" || p.Points[0].Lat != 61 || p.Points[1].Name != "62N020W" || p.Points[1].Lat != 62 || p.Points[2].Name != "PIDSO" {
		t.Errorf("points: %+v (nothing is added for the destination)", p.Points)
	}
}

// A pre-departure clearance is served as its own route, from the origin airport along the cleared fixes, with no destination
// point, and counts from the day and time it was issued, not from when a station delivered it.
func TestPreDepartureClearanceIsServedAsItsOwnRoute(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 16, 0, 0, 0, time.UTC)
	nav := navdata.New()
	nav.Add("WOL", navdata.Point{Lat: -34.56, Lon: 150.79, Kind: "vor"})
	nav.Add("OTKED", navdata.Point{Lat: -36.5, Lon: 151.5, Kind: "fix"})
	nav.Add("STUIE", navdata.Point{Lat: -12.8, Lon: 131.5, Kind: "fix"}) // an older fix of that name, a long way from where the route goes
	dec := `{"fields":{"pdc":{"day":"27","time":"21:48","callsign":"QFA63","origin":"YSSY","dest":"FAOR","runway":"16R","sid":"GROOK2","transition":"WOL",
		"climb_to":5000,"dep_freq":"129.700","squawk":"1372","route":{"callsign":"QFA63","from":"YSSY","to":"","fixes":[{"name":"WOL"},{"name":"OTKED","via":"H20"},
		{"name":"STUIE"},{"name":"3756S13533E","lat":-37.93,"lon":135.55,"has_pos":true}]}}}}`
	// delivered three days late, and twice
	st.AddMessage(store.Message{ID: 1, Hex: "7C4927", T: now.Add(-40 * time.Minute), Label: "RA", Text: "a", Kind: "departure_clearance", Decoded: dec})
	st.AddMessage(store.Message{ID: 2, Hex: "7C4927", T: now.Add(-30 * time.Minute), Label: "RA", Text: "b", Kind: "departure_clearance", Decoded: dec})
	st.AddMessage(store.Message{ID: 3, Hex: "7C4927", T: now.Add(-20 * time.Minute), Label: "RB", Text: "c", Kind: "departure_clearance", Decoded: `{"fields":{"pdc":{"acknowledge":true}}}`})
	ts := httptest.NewServer((&Server{Store: st, Nav: nav, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	_, body := do(t, "GET", ts.URL+"/api/plan/7C4927", "")
	var plans []struct {
		Kind, Detail, From, To string
		T                      time.Time
		ID                     int64
		Missing                []string
		Points                 []struct {
			Name string
			Kind string
		}
	}
	if err := json.Unmarshal([]byte(body), &plans); err != nil || len(plans) != 1 {
		t.Fatalf("%v: %s", err, body)
	}
	p := plans[0]
	if p.Kind != "pdc" || p.From != "YSSY" || p.To != "FAOR" || p.ID != 2 || !p.T.Equal(time.Date(2026, 10, 27, 21, 48, 0, 0, time.UTC)) {
		t.Errorf("pdc: %+v", p)
	}
	if p.Detail != "runway 16R, GROOK2 (WOL) \u00b7 climb 5000 ft \u00b7 departure 129.700 \u00b7 squawk 1372 \u00b7 issued on day 27 at 21:48Z" || len(p.Points) != 4 {
		t.Errorf("detail %q, %d points", p.Detail, len(p.Points))
	}
	if len(p.Missing) != 1 || p.Missing[0] != "STUIE" {
		t.Errorf("a fix of that name 1,300 nm off the route is not drawn: %v", p.Missing)
	}
	if p.Points[0].Name != "YSSY" || p.Points[0].Kind != "airport" || p.Points[1].Name != "WOL" || p.Points[3].Name != "3756S13533E" {
		t.Errorf("points: %+v (the origin, the fixes, and no destination)", p.Points)
	}
}

// "until" fetches a window that ended in the past, so an older flight comes by itself and not as the oldest part of the
// newest rows; it can never reach into the future.
func TestWindowUntil(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	old := now.Add(-5 * 24 * time.Hour) // five days ago: outside a 96 hour window
	st.AddMessage(store.Message{ID: 1, Hex: "A2551D", T: old, Label: "H1", Text: "old flight", Kind: "unknown"})
	st.AddMessage(store.Message{ID: 2, Hex: "A2551D", T: now.Add(-time.Hour), Label: "H1", Text: "recent", Kind: "unknown"})
	st.AddPosition(store.Position{Hex: "A2551D", T: old, Lat: 51, Lon: 0, Source: "x"})
	st.AddPosition(store.Position{Hex: "A2551D", T: now.Add(-time.Hour), Lat: 40, Lon: -100, Source: "x"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	until := old.Add(3 * time.Hour).Format(time.RFC3339)

	_, body := do(t, "GET", ts.URL+"/api/messages/A2551D?hours=96", "")
	if strings.Count(body, `"raw"`) != 1 || strings.Contains(body, "old flight") {
		t.Errorf("96 hours does not reach five days back: %s", body)
	}
	_, body = do(t, "GET", ts.URL+"/api/messages/A2551D?hours=10&until="+until, "")
	if strings.Count(body, `"raw"`) != 1 || !strings.Contains(body, "old flight") {
		t.Errorf("the window before until: %s", body)
	}
	_, body = do(t, "GET", ts.URL+"/api/track/A2551D?hours=10&until="+until, "")
	if strings.Count(body, `"lat"`) != 1 || !strings.Contains(body, `"lat":51`) {
		t.Errorf("track before until: %s", body)
	}
	// an until in the future is the present
	_, body = do(t, "GET", ts.URL+"/api/messages/A2551D?hours=10&until="+now.Add(48*time.Hour).Format(time.RFC3339), "")
	if strings.Count(body, `"raw"`) != 1 || !strings.Contains(body, "recent") {
		t.Errorf("until in the future: %s", body)
	}
}

// Two pieces of a flight that the rules could not tell are one are joined by hand, and taken apart again.
func TestJoinFlightsByHand(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	air := func(at time.Time, cs string, lat float64) {
		alt := 36000.0
		st.AddPosition(store.Position{Hex: "7C4927", T: at, Lat: lat, Lon: 100, AltFt: &alt, Callsign: cs, Source: "x"})
	}
	for i := 0; i < 6; i++ { // two flights with different numbers, 8 hours apart
		air(now.Add(-14*time.Hour).Add(time.Duration(i)*10*time.Minute), "QFA8", 10+float64(i)*0.3)
		air(now.Add(-5*time.Hour).Add(time.Duration(i)*10*time.Minute), "QFA9", -20+float64(i)*0.3)
	}
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	list := func() []struct {
		ID     string  `json:"id"`
		Joined []int64 `json:"joined"`
	} {
		_, body := do(t, "GET", ts.URL+"/api/flights/7C4927?hours=72", "")
		var l []struct {
			ID     string  `json:"id"`
			Joined []int64 `json:"joined"`
		}
		if err := json.Unmarshal([]byte(body), &l); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return l
	}
	before := list()
	if len(before) != 2 {
		t.Fatalf("two flights to begin with: %+v", before)
	}
	second := now.Add(-5 * time.Hour).Unix()
	put := func(body string) (int, string) { return do(t, "PUT", ts.URL+"/api/flights/7C4927/join", body) }

	if code, body := put(fmt.Sprintf(`{"start":%d,"join":true}`, now.Add(-14*time.Hour).Unix())); code != 400 {
		t.Errorf("nothing before the first flight to join it to: %d %s", code, body)
	}
	if code, _ := put(fmt.Sprintf(`{"start":%d,"join":true}`, now.Add(-30*time.Hour).Unix())); code != 404 {
		t.Errorf("no flight then: %d", code)
	}
	if code, body := put(fmt.Sprintf(`{"start":%d,"join":false}`, second)); code != 400 {
		t.Errorf("not joined yet: %d %s", code, body)
	}
	code, body := put(fmt.Sprintf(`{"start":%d,"join":true}`, second))
	if code != 200 || !strings.Contains(body, fmt.Sprintf(`"joined":[%d]`, second)) {
		t.Fatalf("join: %d %s", code, body)
	}
	after := list()
	if len(after) != 1 || len(after[0].Joined) != 1 || after[0].ID != before[0].ID {
		t.Errorf("one flight, starting where the first started: %+v", after)
	}
	if code, body = put(fmt.Sprintf(`{"start":%d,"join":false}`, now.Add(-14*time.Hour).Unix())); code != 200 {
		t.Fatalf("split: %d %s", code, body)
	}
	if again := list(); len(again) != 2 {
		t.Errorf("two again: %+v", again)
	}
}

func TestBackfillEndpoints(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	off := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer off.Close()
	if code, body := do(t, "GET", off.URL+"/api/backfill", ""); code != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Errorf("off: %d %s", code, body)
	}
	if code, body := do(t, "GET", off.URL+"/api/backfill/plan", ""); code != 409 || !strings.Contains(body, "backfill.enabled") {
		t.Errorf("plan while off: %d %s", code, body)
	}
	if code, _ := do(t, "POST", off.URL+"/api/backfill", ""); code != 409 {
		t.Errorf("start while off: %d", code)
	}

	st.AddPosition(store.Position{Hex: "7C4926", T: time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC), Lat: 1, Lon: 1, Source: "adsb.lol"})
	m := &history.Manager{Store: st, Cache: history.Cache{Dir: t.TempDir()}, MaxBytes: 100 << 30, Now: func() time.Time { return now }}
	on := httptest.NewServer((&Server{Store: st, Backfill: m, Now: func() time.Time { return now }}).Handler())
	defer on.Close()
	_, body := do(t, "GET", on.URL+"/api/backfill", "")
	var status struct {
		Enabled    bool  `json:"enabled"`
		CacheLimit int64 `json:"cache_limit_bytes"`
	}
	if json.Unmarshal([]byte(body), &status) != nil || !status.Enabled || status.CacheLimit != 100<<30 {
		t.Errorf("on: %s", body)
	}
	_, body = do(t, "GET", on.URL+"/api/backfill/plan?hex=7c4926", "")
	var plan struct {
		Days     []struct{ Day string }
		Aircraft int
	}
	if json.Unmarshal([]byte(body), &plan) != nil || plan.Aircraft != 1 || len(plan.Days) != 2 || plan.Days[0].Day != "2026-10-31" {
		t.Errorf("plan: %s", body)
	}
	_, body = do(t, "GET", on.URL+"/api/backfill/plan?days=2026-10-30", "")
	var sel struct {
		Days []struct {
			Day      string
			Selected bool
		}
		Download int64 `json:"download_bytes"`
	}
	if json.Unmarshal([]byte(body), &sel) != nil || len(sel.Days) != 2 || sel.Download != history.EstDayBytes {
		t.Errorf("a selection: %s", body)
	}
	for _, d := range sel.Days {
		if d.Selected != (d.Day == "2026-10-30") {
			t.Errorf("selected: %s", body)
		}
	}
	if code, body := do(t, "GET", on.URL+"/api/backfill/plan?days=1999-01-01", ""); code != 400 || !strings.Contains(body, "1999-01-01") {
		t.Errorf("a day that cannot be read: %d %s", code, body)
	}
	if code, body := do(t, "POST", on.URL+"/api/backfill", `{"days":[]}`); code != 400 || !strings.Contains(body, "no days chosen") {
		t.Errorf("no days chosen: %d %s", code, body)
	}
	if _, body = do(t, "GET", on.URL+"/api/backfill/plan?hex=ABCDEF", ""); !strings.Contains(body, `"days":[]`) {
		t.Errorf("an aircraft with no data plans no days: %s", body)
	}
	if code, _ := do(t, "POST", on.URL+"/api/backfill", `{"hex":"nonsense"}`); code != 400 {
		t.Errorf("bad hex: %d", code)
	}
	if code, body := do(t, "POST", on.URL+"/api/backfill", `{"hex":"ABCDEF"}`); code != 400 || !strings.Contains(body, "nothing to do") {
		t.Errorf("nothing to do: %d %s", code, body)
	}
}

func TestFlightWatchesShowALookupHold(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"CKK624", "B-LRM"} {
		e, _ := watchlist.Parse(id, now, time.Time{})
		st.AddWatch(e)
	}
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()
	if _, body := do(t, "GET", ts.URL+"/api/watches", ""); strings.Contains(body, "lookup_hold") {
		t.Errorf("a hold from nowhere: %s", body)
	}
	st.SetProviderHold("aerodatabox", now.Add(2*time.Hour), "You have exceeded the MONTHLY quota")
	_, body := do(t, "GET", ts.URL+"/api/watches", "")
	var ws []struct {
		Ident string
		Hold  *struct{ Reason string } `json:"lookup_hold"`
	}
	if json.Unmarshal([]byte(body), &ws) != nil || len(ws) != 2 {
		t.Fatal(body)
	}
	for _, w := range ws {
		if (w.Ident == "CKK624") != (w.Hold != nil) {
			t.Errorf("%s: hold %+v (only a flight watch uses the flight-number lookup)", w.Ident, w.Hold)
		}
	}
}

func TestLateMessagesCarryWhenTheyCount(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)
	sent := time.Date(2026, 10, 27, 5, 0, 0, 0, time.UTC)
	st.AddMessage(store.Message{ID: 1, Hex: "780AA4", T: now.Add(-time.Hour), Label: "H1", Text: "a", Kind: "miam", Flight: "CX301",
		Decoded: `{"fields":{"a350":{"Time":"2026-10-27T05:00:00Z","Origin":"VHHH","Dest":"EDDM"}}}`})
	st.AddMessage(store.Message{ID: 2, Hex: "780AA4", T: now.Add(-30 * time.Minute), Label: "B9", Text: "b", Kind: "datis_request"})
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	// in the window of the flight it was sent in: listed, with when it arrived and when it counts
	_, body := do(t, "GET", ts.URL+"/api/messages/780AA4?hours=3&until=2026-10-27T06:00:00Z", "")
	var ms []struct {
		ID int64
		T  time.Time
		At *time.Time `json:"at"`
	}
	if json.Unmarshal([]byte(body), &ms) != nil || len(ms) != 1 || ms[0].ID != 1 || ms[0].At == nil || !ms[0].At.Equal(sent) || !ms[0].T.Equal(now.Add(-time.Hour)) {
		t.Fatalf("%s", body)
	}
	// in the window it arrived in: only the one that counts from its arrival, which says nothing extra
	_, body = do(t, "GET", ts.URL+"/api/messages/780AA4?hours=3", "")
	var later []struct {
		ID int64
		At *time.Time `json:"at"`
	}
	if json.Unmarshal([]byte(body), &later) != nil || len(later) != 1 || later[0].ID != 2 || later[0].At != nil {
		t.Errorf("%s", body)
	}
}

// Messages with nothing to place them but their arrival time, which airframes.io tagged with an earlier flight of the aircraft,
// are marked as belonging there: B-LRD's D-ATIS requests for Munich, delivered by a Slovak ground station during its flight to Hong Kong.
func TestMessagesTaggedWithAnotherFlightAreMarked(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, 27+d, h, m, 0, 0, time.UTC) }
	for i, x := 0, day(0, 4, 0); !x.After(day(0, 12, 0)); i, x = i+1, x.Add(10*time.Minute) {
		st.AddPosition(store.Position{Hex: "780AA4", T: x, Lat: 30 + float64(i)/100, Lon: 60, Callsign: "CPA301", Source: "x"})
	}
	for i, x := 0, day(0, 20, 0); !x.After(day(1, 6, 0)); i, x = i+1, x.Add(10*time.Minute) {
		st.AddPosition(store.Position{Hex: "780AA4", T: x, Lat: 40 + float64(i)/100, Lon: -150, Callsign: "CPA833", Source: "x"})
	}
	st.AddMessage(store.Message{ID: 1, Hex: "780AA4", T: day(1, 2, 19), Label: "B9", Text: "a", Kind: "datis_request", AFFlight: "CX0301", SourceType: "vdl"})
	st.AddMessage(store.Message{ID: 2, Hex: "780AA4", T: day(1, 3, 23), Label: "B9", Text: "b", Kind: "datis_request", AFFlight: "CX0833", SourceType: "vdl"})
	st.AddMessage(store.Message{ID: 3, Hex: "780AA4", T: day(1, 3, 30), Label: "B9", Text: "c", Kind: "datis_request", SourceType: "vdl"})
	// the same tag over a satellite link, which stamps a message as it arrives: left alone
	st.AddMessage(store.Message{ID: 4, Hex: "780AA4", T: day(1, 3, 40), Label: "B9", Text: "d", Kind: "datis_request", AFFlight: "CX0301", SourceType: "aero-acars"})
	now := day(2, 0, 0)
	ts := httptest.NewServer((&Server{Store: st, Now: func() time.Time { return now }}).Handler())
	defer ts.Close()

	_, body := do(t, "GET", ts.URL+"/api/messages/780AA4?hours=24&until=2026-10-28T06:00:00Z", "")
	var ms []struct {
		ID        int64
		Elsewhere *struct{ Flight, ID string }
	}
	if json.Unmarshal([]byte(body), &ms) != nil || len(ms) != 4 {
		t.Fatalf("%s", body)
	}
	if e := ms[0].Elsewhere; e == nil || e.Flight != "CX0301" || !strings.HasPrefix(e.ID, "CX301@") {
		t.Errorf("tagged with the flight before: %s", body)
	}
	if ms[1].Elsewhere != nil || ms[2].Elsewhere != nil || ms[3].Elsewhere != nil {
		t.Errorf("its own flight's, one with no tag, and one over a satellite link stay where they are: %s", body)
	}
}

type fakeStationList struct {
	list  []airframes.Station
	err   error
	calls int
}

func (f *fakeStationList) Stations(context.Context) ([]airframes.Station, error) {
	f.calls++
	return f.list, f.err
}

func TestStationsScreen(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	now := time.Date(2026, 11, 1, 22, 0, 0, 0, time.UTC)
	sent := now.Add(-48 * time.Hour)
	a350 := func(at time.Time) string {
		return `{"fields":{"a350":{"Time":"` + at.Format("2006-01-02T15:04:05Z") + `","Origin":"VHHH","Dest":"EDDM"}}}`
	}
	for i := 0; i < 8; i++ { // a station that is always a day late
		at := sent.Add(time.Duration(i) * time.Minute)
		st.AddMessage(store.Message{ID: int64(i + 1), Hex: "780AA4", T: at.Add(24 * time.Hour), Label: "H1", Text: fmt.Sprint("a", i), Kind: "miam", Decoded: a350(at), Station: "EB-LZIB", SourceType: "vdl"})
	}
	for i := 0; i < 6; i++ { // one that is prompt
		at := sent.Add(time.Duration(i) * time.Minute)
		st.AddMessage(store.Message{ID: int64(100 + i), Hex: "780AB8", T: at.Add(4 * time.Second), Label: "H1", Text: fmt.Sprint("b", i), Kind: "miam", Decoded: a350(at), Station: "KBFI-1", SourceType: "vdl"})
	}
	st.AddMessage(store.Message{ID: 200, Hex: "780AB8", T: sent, Label: "B9", Text: "c", Kind: "datis_request", Station: "NEW-1", SourceType: "acars"}) // nothing timed

	lat, lon := 48.1486, 17.10775
	src := &fakeStationList{list: []airframes.Station{
		{Ident: "EB-LZIB", Latitude: &lat, Longitude: &lon, Fuzzed: true, CountryName: "Slovakia", CountryCode: "SK", SourceType: "vdl", Application: "dumpvdl2", Flagged: true,
			FlaggedReason: "Rate anomaly detected", MessagesCount: 235660243, NearestAirport: "LZIB", LastReportAt: now},
		{Ident: "KBFI-1", SourceType: "vdl", Application: "dumpvdl2"}, // no position
		{Ident: "SOMEONE-ELSE", Latitude: &lat, Longitude: &lon},
	}}
	srv := &Server{Store: st, StationList: src, Now: func() time.Time { return now }}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	type stj struct {
		Ident         string         `json:"ident"`
		Verdict       string         `json:"verdict"`
		Messages      int            `json:"messages"`
		Timed         int            `json:"timed"`
		Late          int            `json:"late"`
		LateFrac      float64        `json:"late_frac"`
		MedianS       int64          `json:"median_s"`
		ShortestS     int64          `json:"shortest_s"`
		Known         bool           `json:"known"`
		Fuzzed        bool           `json:"fuzzed"`
		Flagged       bool           `json:"flagged"`
		Lat           *float64       `json:"lat"`
		Country       string         `json:"country"`
		Application   string         `json:"application"`
		FlaggedReason string         `json:"flagged_reason"`
		Transports    map[string]int `json:"transports"`
	}
	var body struct {
		Fetched  *time.Time
		CanFetch bool `json:"can_fetch"`
		Stations []stj
	}
	get := func(method, path string) (int, string) {
		code, b := do(t, method, ts.URL+path, "")
		body.Fetched, body.Stations = nil, nil
		json.Unmarshal([]byte(b), &body)
		return code, b
	}

	// before the list has been fetched: ours only, ordered by how many messages, with the lateness we measured
	if code, b := get("GET", "/api/stations"); code != 200 || len(body.Stations) != 3 || body.Fetched != nil || !body.CanFetch {
		t.Fatalf("%d %s", code, b)
	}
	by := func() map[string]stj {
		m := map[string]stj{}
		for _, s := range body.Stations {
			m[s.Ident] = s
		}
		return m
	}
	late := by()["EB-LZIB"]
	if late.Verdict != "late" || late.Timed != 8 || late.Late != 8 || late.LateFrac != 1 || late.MedianS != 86400 || late.ShortestS != 86400 || late.Known || body.Stations[0].Ident != "EB-LZIB" {
		t.Errorf("%+v", late)
	}
	if by()["KBFI-1"].Verdict != "prompt" || by()["NEW-1"].Verdict != "unknown" || by()["NEW-1"].Transports["acars"] != 1 {
		t.Errorf("%+v %+v", by()["KBFI-1"], by()["NEW-1"])
	}

	// fetch the list: only the stations we have heard from are kept, with where they are
	if code, b := get("POST", "/api/stations/refresh"); code != 200 || body.Fetched == nil || src.calls != 1 {
		t.Fatalf("%d %s", code, b)
	}
	late = by()["EB-LZIB"]
	if !late.Known || late.Lat == nil || *late.Lat != 48.1486 || !late.Fuzzed || !late.Flagged || late.FlaggedReason != "Rate anomaly detected" || late.Country != "Slovakia" || late.Application != "dumpvdl2" {
		t.Errorf("%+v", late)
	}
	if by()["KBFI-1"].Lat != nil || !by()["KBFI-1"].Known || by()["NEW-1"].Known || len(body.Stations) != 3 {
		t.Errorf("%+v", body.Stations)
	}
	// again at once: refused, and the source is not asked
	if code, b := get("POST", "/api/stations/refresh"); code != 429 || !strings.Contains(b, "fetched") || src.calls != 1 {
		t.Errorf("%d %s calls %d", code, b, src.calls)
	}
}

func TestStationsRefreshProblems(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	st.AddMessage(store.Message{ID: 1, Hex: "780AA4", T: time.Now(), Label: "H1", Text: "a", Station: "EB-LZIB"})
	now := time.Now()
	for name, c := range map[string]struct {
		src  StationSource
		want int
		text string
	}{
		"no source":     {nil, 409, "no source"},
		"it fails":      {&fakeStationList{err: errors.New("airframes.io: stations: HTTP 503")}, 502, "503"},
		"not our names": {&fakeStationList{list: []airframes.Station{{Ident: "OTHER"}}}, 502, "has its layout changed"},
	} {
		srv := &Server{Store: st, Now: func() time.Time { return now }}
		if c.src != nil {
			srv.StationList = c.src
		}
		ts := httptest.NewServer(srv.Handler())
		code, b := do(t, "POST", ts.URL+"/api/stations/refresh", "")
		ts.Close()
		if code != c.want || !strings.Contains(b, c.text) {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	if !st.StationsFetched().IsZero() {
		t.Error("a failed fetch was noted as done")
	}
}
