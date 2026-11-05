package decoder

import (
	"strings"
	"testing"
	"time"
)

// Real messages from N781AV, an Avianca 787. The ones received late (the 28th, about four hours) keep the time inside them.
func TestAviancaReports(t *testing.T) {
	recv := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04:05", s); return v }
	posrpt := "3N01 POSRPT 4907/LEMD/SKBO .N781AV/03A17:11\r\n/WPY AVILA/NWYP LONGA /HDG -88.79/MCH    .47\r\n/POS N40382 W003548/FL 12205/TAS 303/SAT +  1\r\n/SWND     7/DWND -  14/FOB 53800/ETA 0226"
	r, err := Decode(Message{Label: "83", Text: posrpt})
	if err != nil || r.Kind != KindOps || r.Pos == nil {
		t.Fatalf("%v %v %+v", err, r.Kind, r)
	}
	if r.Summary != "Avianca AV4907 position report LEMD→SKBO 40.637N 3.913W, FL122, M0.470" || r.Identity.Tail != "N781AV" || r.Identity.Flight != "AV4907" {
		t.Errorf("%q %+v", r.Summary, r.Identity)
	}
	if got := r.Pos.At(recv("2026-10-28 21:05:36")); !got.Equal(time.Date(2026, 10, 28, 17, 11, 0, 0, time.UTC)) {
		t.Errorf("placed at %v, want the time inside it", got)
	}
	if *r.Pos.AltFt != 12205 || *r.Wx.TempC != 1 || *r.Wx.WindKt != 7 || *r.Wx.WindDirDeg != 346 { // -14 blows from 346
		t.Errorf("alt %v wx %+v", *r.Pos.AltFt, r.Wx)
	}
	if o := r.Fields["ops"].(OpsReport); o.ETA != "0226" || o.Info["fob"] != "53800" || o.Info["next"] != "LONGA" || o.Origin != "LEMD" {
		t.Errorf("%+v", o)
	}
	r, _ = Decode(Message{Label: "83", Text: "3N01 POSRPT 0182/SKBO/LEMD .N781AV/03A06:37\r\n/WPY ROLDO/NWYP RUKER /HDG  82.58/MCH    .81\r\n/POS N39509 W005522/FL 38328/TAS 460/SAT - 58\r\n/SWND    32/DWND    42/FOB  8900/ETA 0703"})
	if r.Pos == nil || *r.Wx.TempC != -58 || *r.Wx.WindDirDeg != 42 || *r.Wx.WindKt != 32 || r.Identity.Flight != "AV182" || r.Summary != "Avianca AV182 position report SKBO→LEMD 39.848N 5.870W, FL383, M0.810" {
		t.Errorf("%q %+v", r.Summary, r.Wx)
	}
	// the Delta format on label 80 is still its own
	if r, _ = Decode(Message{Label: "80", Text: "3N01 POSRPT 0039/30 KLAX/ZSPD .N501DN\n/POS N34414W119598/ALT 252/MCH 759/FOB 2022\n/TME 1941/WND 239 032/OAT -029/TAS 0462/ETA 0925"}); r.Kind != KindPOSRPT {
		t.Errorf("Delta: %v", r.Kind)
	}

	// label 99: the time to the second, the position, the altitude, the Mach number
	emr := "EMR05/06:31:44,N39537W005182,31541,0.804,295,-17,461\r\n082,085,081,084\r\n8.6,32.8,9.0,33.4\r\n8200,+.1,+.0,-7778,-5.8,,,+.0\r\nL/G OFF,AP ENG,FLAP LVR 0,A/I AUTO"
	r, _ = Decode(Message{Label: "99", Text: emr})
	if r.Kind != KindOps || r.Pos == nil || r.Summary != "Avianca position report 39.895N 5.303W, FL315, M0.804" || *r.Pos.AltFt != 31541 {
		t.Fatalf("%v %q", r.Kind, r.Summary)
	}
	if got := r.Pos.At(recv("2026-10-31 06:31:44")); !got.Equal(time.Date(2026, 10, 31, 6, 31, 44, 0, time.UTC)) {
		t.Errorf("placed at %v", got)
	}
	if o := r.Fields["ops"].(OpsReport); o.Info["tas"] != "461" {
		t.Errorf("%+v", o.Info)
	}

	// label 24: the takeoff report gives the departure time, and its fuel in kg or in tonnes
	r, _ = Decode(Message{Label: "24", Text: "TKO01   AVA4907/28281720/LEMD/SKBO\r\n/DT 1656/TO 1706/TOF  523\r\n/DEP 3/FLAP  5/TPR ***TKO****\r\nTOF 54500KG\r\n"})
	o, _ := r.Fields["ops"].(OpsReport)
	if r.Kind != KindOps || o.Out != "1656" || o.Off != "1706" || o.Origin != "LEMD" || o.Info["takeoff_fuel_kg"] != "54500" || r.Summary != "Avianca AV4907 takeoff report LEMD→SKBO: 54,500 kg of fuel, flaps 5" {
		t.Errorf("%v %q %+v", r.Kind, r.Summary, o)
	}
	if !strings.Contains(r.Text, "out 16:56Z, off 17:06Z") || r.Pos != nil {
		t.Errorf("%q", r.Text)
	}
	r, _ = Decode(Message{Label: "24", Text: "TKO01    AVA183/31310945/LEMD/SKBO\r\n/DT 0920/TO 0935/TOF  551\r\n/DEP 1/FLAP  5/TPR ***TKO****\r\nTOF 57.0 KG\r\n"})
	if o, _ = r.Fields["ops"].(OpsReport); o.Info["takeoff_fuel_kg"] != "57000" || o.Flight != "AV183" {
		t.Errorf("%+v", o)
	}

	// EB: one position, with its temperature (-30) and wind (339 degrees, 16 kt)
	r, _ = Decode(Message{Label: "H1", Text: "EB0032AA_ N781AV LEMDSKBO 36\r\n 40307-0050761720 24824-301339016010/9W003004"})
	if r.Kind != KindOps || r.Pos == nil || r.Summary != "Avianca position report LEMD→SKBO 40.307N 5.076W, FL248" || r.Identity.Tail != "N781AV" {
		t.Fatalf("%v %q", r.Kind, r.Summary)
	}
	if *r.Wx.TempC != -30 || *r.Wx.WindDirDeg != 339 || *r.Wx.WindKt != 16 || r.Pos.Secs != 17*3600+20*60 {
		t.Errorf("%+v %v", r.Wx, r.Pos.Secs)
	}
	// a plus temperature is a space; low and high altitude
	r, _ = Decode(Message{Label: "H1", Text: "EB0032AA_ N781AV SKBOLEMD107\r\n 40153-0037730642 09867 036354005010NXW004006"})
	if r.Pos == nil || *r.Wx.TempC != 3 || *r.Wx.WindDirDeg != 354 || *r.Wx.WindKt != 5 || *r.Pos.AltFt != 9867 {
		t.Errorf("%+v", r.Wx)
	}
	// EB0031: a position a minute, the last line cut short by the size of the block. The newest is the position.
	r, _ = Decode(Message{Label: "H1", Text: "EB0031AA_ N781AV SKBOLEMD1\r\n 40229-0034561208 04458 10031100801005Z009014\r\n 40256-0033841209 03765 11831100808PYY,072121\r\n 40310-0033681210 03758 11425900300X9B,082118\r\n 40358-0034061211 03772 11229600400XV"})
	if r.Pos == nil || r.Pos.Secs != 12*3600+11*60 || *r.Pos.AltFt != 3772 || *r.Wx.WindDirDeg != 296 || !strings.Contains(r.Summary, "the newest of 4 positions") {
		t.Fatalf("%q %+v", r.Summary, r.Pos)
	}
	if !strings.Contains(r.Text, "12:08Z 40.229N 3.456W, 4,458 ft") || !strings.Contains(r.Text, "12:11Z") {
		t.Errorf("%q", r.Text)
	}
	// the other EB report and a maintenance dump are still undecoded
	for _, text := range []string{"EB0021CA_8   1\r\n N781AVAVA1TO 234SKBOLEMD\r\nBSM5170990028  647422972 8062 87", "CFG422004 2 28OCT26 1707 AV N781AV AVA4907 LEMD/SKBO BCG4F-45LD-0077 C L"} {
		if r, _ = Decode(Message{Label: "H1", Text: text}); r.Kind != KindUnknown {
			t.Errorf("%.20q decoded as %v", text, r.Kind)
		}
	}
	// a position line that cannot be read makes the whole message something else
	if r, _ = Decode(Message{Label: "H1", Text: "EB0032AA_ N781AV SKBOLEMD107\r\n 4015X-0037730642 09867 036354005010NXW004006"}); r.Kind != KindUnknown {
		t.Errorf("bad line decoded as %v", r.Kind)
	}
}

// Avianca's progress report has no TS time, and a hex check on the end.
func TestAviancaProgress(t *testing.T) {
	r, _ := Decode(Message{Label: "H1", Text: "PRG/DTLEMD,32L,186,070523,046/FNAVA18231AA"})
	if r.Kind != KindProgress || r.Summary != "Progress report AVA182 to LEMD runway 32L, ETA 07:05:23Z" {
		t.Errorf("%v %q", r.Kind, r.Summary)
	}
}
