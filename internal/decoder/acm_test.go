package decoder

import "testing"

// Real aircraft condition monitoring reports (Air France, KLM, Air Canada), a position report from TC-NCE, and A380 flight data link
// messages from a British Airways aircraft.
const (
	text_acm_af211  = "ACM01ACM211BSF-GSQL  AFR98E  LFPGKIAH131126  67TR120825148223112ANTACM40\r\n 685030251 0 01020664520\r\n000000000000906898100200A\r\n"
	text_acm_af230  = "ACM01ACM230FSF-GSQL  AFR003  KJFKLFPG131126  66ER063028458313112ANTACM40\r\n 689068981002\r\n9062841002\r\n"
	text_acm_klm211 = "ACM01ACM211BSPH-BQI  KLM809  EHAMWMKK091126 124TR193858 8561310BKLMACM59\r\n172503013) 0 01020631640\r\n"
	text_acm_klm108 = "ACM01ACM108BSAPH-BQI  KLM809  EHAMWMKK091126 124PO191620 72033164BSM79949\r\n274 15462419724368E  103  103\r\n"
	text_acm_aca    = "ACM01ACM108BSAC-FIUR  ACA858  EGLLEGLL311026  19TI10313040486316ABSM79965\r\n 37 18852165821271A  136  138\r\n"
	text_pc_cruise  = "PC25AG/09NOV26 16:17:00,34974,1648,  90,N 41.667 E 27.006"
	text_pc_west    = "PC74PL/07NOV26 11:18:00,8514,1125, 106,N 52.045 E   .794"
)

func TestConditionMonitoringReports(t *testing.T) {
	for _, c := range []struct {
		text, summary, flight, callsign, tail, route, at string
	}{
		{text_acm_af211, "Air France condition monitoring report 211 AFR98E LFPG→KIAH at 12:08Z", "AF98E", "AFR98E", "F-GSQL", "LFPG>KIAH", "2026-11-13T12:08:25Z"},
		{text_acm_af230, "Air France condition monitoring report 230 AFR3 KJFK→LFPG at 06:30Z", "AF3", "AFR3", "F-GSQL", "KJFK>LFPG", "2026-11-13T06:30:28Z"},
		{text_acm_klm211, "KLM condition monitoring report 211 KLM809 EHAM→WMKK at 19:38Z", "KL809", "KLM809", "PH-BQI", "EHAM>WMKK", "2026-11-09T19:38:58Z"},
		{text_acm_klm108, "KLM condition monitoring report 108 KLM809 EHAM→WMKK at 19:16Z", "KL809", "KLM809", "PH-BQI", "EHAM>WMKK", "2026-11-09T19:16:20Z"}, // a stray A before the registration
		{text_acm_aca, "Air Canada condition monitoring report 108 ACA858 at 10:31Z", "AC858", "ACA858", "C-FIUR", ">", "2026-10-31T10:31:30Z"},               // on the ground: EGLL to EGLL is no route
	} {
		r, err := Decode(Message{Label: "H1", Text: c.text})
		if err != nil {
			t.Fatal(err)
		}
		o, _ := r.Fields["ops"].(OpsReport)
		if r.Kind != KindOps || r.Summary != c.summary || o.Flight != c.flight || o.Callsign != c.callsign || o.Reg != c.tail || o.Origin+">"+o.Dest != c.route || o.At != c.at {
			t.Errorf("%s:\n%v %q\n%+v", c.summary, r.Kind, r.Summary, o)
		}
		if r.Identity.Tail != c.tail || r.Identity.Flight != c.flight {
			t.Errorf("identity %+v", r.Identity)
		}
	}
}

func TestPositionReportOnLabel16(t *testing.T) {
	r, err := Decode(Message{Label: "16", Text: text_pc_cruise})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindAOC || r.Pos == nil || r.Pos.Lat != 41.667 || r.Pos.Lon != 27.006 || r.Pos.AltFt == nil || *r.Pos.AltFt != 34974 || r.Pos.Basis != BasisDate || r.Pos.Secs != 11091617 {
		t.Fatalf("%v %+v", r.Kind, r.Pos)
	}
	if want := "Airline position report 41.667N 27.006E FL350"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
	// a longitude under one degree has no zero in front of it
	r, _ = Decode(Message{Label: "16", Text: text_pc_west})
	if r.Pos == nil || r.Pos.Lon != 0.794 || r.Pos.Lat != 52.045 {
		t.Errorf("%+v", r.Pos)
	}
	// the message is not mistaken for another airline's position report, and a position that cannot be is left out
	if r, _ = Decode(Message{Label: "16", Text: "PC25AG/09NOV26 16:17:00,34974,1648,  90,N 91.667 E 27.006"}); r.Pos != nil {
		t.Errorf("a latitude of 91 degrees: %+v", r.Pos)
	}
}

func TestFlightDataLinkIsNotQantasEverywhere(t *testing.T) {
	r, _ := Decode(Message{Label: "RF", Text: ",FDAACK01261109231725/329633,342,,1,608B"})
	if r.Kind != KindOps || r.Summary != "Flight data acknowledged" {
		t.Errorf("an acknowledgement says whose it is only if the message does: %v %q", r.Kind, r.Summary)
	}
	r, _ = Decode(Message{Label: "RF", Text: ",FDASUB01261106171504/342811,365,,1,98A9"})
	if r.Summary != "Flight data submitted" {
		t.Errorf("%q", r.Summary)
	}
	// where it has a flight, the airline follows from the number
	r, _ = Decode(Message{Label: "RF", Text: ",FDASUB01261101034409,LHR,,SIN,,BA0011,261101,0530,388,,G-XLEI,/351247,361,,1,AC2A"})
	if want := "British Airways flight data submitted: BA11 EGLL→WSSS, scheduled 05:30Z"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
}
