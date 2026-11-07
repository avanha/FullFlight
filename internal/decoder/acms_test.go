package decoder

import (
	"strings"
	"testing"
)

// Real American A321 reports (N900UW) and an Emirates A380 maintenance system report.
const (
	text_trp291 = "A321,051035,1,1,TB000000/REP291,00,00,4/\r\nTRP KORD KSEA  8 8\r\n/A1 054101, 42.9940,- 94.9386,360,156.0,454,0.000235,\r\n0.000175, 29.52,- 68.79, 25.8,-56, 6.72,0\r\n/A2 054401, 43.1112,- 95.3942,360,155.7,453,0.000080,\r\n0.000061, 26.41,- 60.45, 25.5,-56, 6.71,0\r\n/A3 054700, 43.2283,- 95.8572,360,155.4,457,0.000099,\r\n0.000042, 23.91,- 71.34, 25.2,-55, 6.70,0\r\n/A4 055001, 43.3441,- 96.3244,360,155.0,457,0.000077,\r\n0.000038, 24.47,- 61.70, 24.8,-55, 6.71,0\r\n/A5 055301, 43.4238,- 96.8077,360,154.8,453,0.000195,\r\n0.000136, 21.83,- 36.17, 24.6,-56, 6.72,0\r\n"
	text_trp191 = "A321,050871,1,1,TB000000/REP191,00,00,4/\r\nTRP KDFW KPHX  9 0\r\n154327, 33.6062,-111.8749, 99,160.2,247,0.007259,0.000794,  2.31,\r\n-150.17,  9.0,  6,20.62,0,0.05113, 1.160, 0.844,-0.010,-0.016,\r\n 1,210, 0,11\r\n"
	text_rep001 = "A321,051018,1,1,TB000000/REP001,00,00,4/CCN900UW,NOV07,020522,KRSW,KORD,0669/C0TIA05AAL031001/C106,95902,5000,11,0010,0,0100,11,X/CE-245,38003,245,777,1355,192,I61L03/EC018370,04480,01766,73,14/EE018487,02284,00881,73/N11406,1406,4330,0818,0847,0262,07026/N21406,1406,3976,0818,0855,0248,07022/S111639,0594,1179,4157,06240,064/S211208,0543,1192,4119,06258,069/T1100,085,00,029,38,202,04444,-229/T2100,083,00,019,39,160,04442,-249/V103D04,00208,08000,228,116,-078/V203C04,00208,08000,244,118,-066/V3017,004,050/V4008,001,289/:"
	text_rep035 = "A321,050882,1,1,TB000000/REP035,00,00,4//C1035,APU RUN/C2N900UW,261105171819,171819,KPHX,KCMH,AAL1152,4200/C3261105171819,261105,171819, 2,D10SAIA05CAAL03.10.01/C4  28.8,  1052,---.-,184320, 32960, 33.4318,-112.0196/C5 23.4, 23.0,X,X,X,X,X, 23.1/C6 4207,EEEEEE,EEEEEE,EEEEEE,EEEEEE,STOP   /C7155612,171819,012207,  0,  1,200.3/C8 1,0,0,1,1,0,1,1,0,0, 1/C9053826,124830,0,X,X,  1127,   960/C00,0,X,X,X,X,0,0,1,1,0,1,RUNNING,RUNNING/E1165313,121746,X,X,X,X,162408,000000,163648,165244,000000/E2X,X.XXX,-.---,1.333,X,X/E3X,  25.5,X,  25.0,X,X/E4X, 13123,  6135, 13687,X,     0,     0,X"
	text_cms    = "<CMS><IH>8|00001|00001|A6-EEV|UAE3HJ|20261106|2241|ABF3AA8FNCL0010|ABF51A8FLAL009S|ABM42CMC012101B|31|L3F</IH><XFRD delta=\"0\"><CH>20261106|2239||YSSY|NZCH|3|391|||</CH><CI f=\"0\" id=\"3160F3JD_1_1\"><T>Cockpit items|1</T><PA><FCA>1|||</FCA></PA><FC><MI>1</MI><FM ref=\"1\">3160F3JD|20261105|055023|1|1|1|DU-MFD,CAPT(4WK1)|CDS|0|1||||1|3|||<C5 counter=\"1\"><FDA>3160W170|CDS CAPT MFD DU FAULT</FDA></C5></FM></FC></CI><CI f=\"0\" id=\"2472F4P1_1_1\"><T>Cockpit items|1</T><PA><FCA>1|||</FCA></PA><FC><MI>1</MI><SP>Electric System Page|107|13|||</SP><FM ref=\"1\">2472F4P1|20261105|204234|1|1|6|LC-BAT1(990PB1) / WRG TO PCDM-1.2(1001XZ2) / PCDM-1.2(1001XZ2)|PEPDC 1.2|3|1||||1|4|||</FM></FC></CI><CI f=\"0\" id=\"3070F0EQ_1_2\"><T>Cabin items|2</T><PA><FCA>1|||</FCA></PA><FC><MI>1</MI><FM ref=\"1\">3070F0EQ|20261106|062749|1|3|2|HEATER-ICE PROTECTION(501) / IPCU-ICE PROTECTION CONTROL UNIT(400DW)|IPCU|0|1||||1|2|||</FM></FC></CI></XFRD></CMS>"
)

func TestAirbusACMSReports(t *testing.T) {
	r, err := Decode(Message{Label: "H1", Text: text_trp291})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := r.Fields["ops"].(OpsReport)
	if r.Kind != KindOps || o.Origin != "KORD" || o.Dest != "KSEA" || r.Pos == nil || r.Pos.Lat != 43.4238 || r.Pos.Lon != -96.8077 || o.Info["points"] != "5" {
		t.Fatalf("trajectory: %v %+v pos %+v", r.Kind, o, r.Pos)
	}
	if want := "A321 trajectory report KORD→KSEA at 05:53Z: 43.424N 96.808W (newest of 5 points)"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
	if !strings.Contains(r.Text, "05:41:01Z 42.994N 94.939W, heading 360°") {
		t.Errorf("text %q", r.Text)
	}
	// the first point of an arrival's report has no number in front of it
	r, _ = Decode(Message{Label: "H1", Text: text_trp191})
	if r.Pos == nil || r.Pos.Lat != 33.6062 || r.Pos.Lon != -111.8749 {
		t.Errorf("arrival trajectory pos %+v", r.Pos)
	}

	r, _ = Decode(Message{Label: "H1", Text: text_rep001})
	o, _ = r.Fields["ops"].(OpsReport)
	if o.Reg != "N900UW" || o.Callsign != "AAL669" || o.Flight != "AA669" || o.Origin != "KRSW" || o.Dest != "KORD" || o.Info["day"] != "07" || o.Info["month"] != "NOV" || r.Identity.Tail != "N900UW" {
		t.Fatalf("report 001: %+v", o)
	}
	if want := "A321 report 001 AAL669 KRSW→KORD"; r.Summary != want {
		t.Errorf("summary %q", r.Summary)
	}

	r, _ = Decode(Message{Label: "H1", Text: text_rep035})
	o, _ = r.Fields["ops"].(OpsReport)
	if o.Info["title"] != "APU RUN" || o.Callsign != "AAL1152" || o.Origin != "KPHX" || o.Dest != "KCMH" || o.At != "2026-11-05T17:18:19Z" {
		t.Fatalf("report 035: %+v", o)
	}
	if want := "A321 report 035: APU RUN AAL1152 KPHX→KCMH"; r.Summary != want {
		t.Errorf("summary %q", r.Summary)
	}
}

func TestA380MaintenanceSystemReport(t *testing.T) {
	r, err := Decode(Message{Label: "H1", Text: text_cms})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := r.Fields["cms"].(CMSReport)
	if r.Kind != KindMaint || !ok || c.Tail != "A6-EEV" || c.Callsign != "UAE3HJ" || c.Origin != "YSSY" || c.Dest != "NZCH" || len(c.Items) != 3 {
		t.Fatalf("%v %+v", r.Kind, c)
	}
	if c.Items[0].Advisory != "CDS CAPT MFD DU FAULT" || c.Items[0].Component != "DU-MFD,CAPT(4WK1)" || c.Items[0].System != "CDS" || c.Items[2].Class != "Cabin items" {
		t.Errorf("items %+v", c.Items)
	}
	if want := "A380 maintenance system report: CDS CAPT MFD DU FAULT and 2 more (UAE3HJ YSSY→NZCH)"; r.Summary != want || r.Identity.Tail != "A6-EEV" {
		t.Errorf("summary %q", r.Summary)
	}
	if !strings.Contains(r.Text, "CDS CAPT MFD DU FAULT (DU-MFD,CAPT(4WK1)) [cockpit items, system CDS, 2026-11-05 05:50:23Z, code 3160F3JD]") {
		t.Errorf("text %q", r.Text)
	}
}
