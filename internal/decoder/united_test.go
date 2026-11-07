package decoder

import (
	"strings"
	"testing"
)

// Real messages from United's dispatch, label RA (the crew's names left out).

func united(t *testing.T, text string) (Result, OpsReport) {
	t.Helper()
	r, err := Decode(Message{Label: "RA", Dir: Uplink, Text: strings.ReplaceAll(text, "\n", "\r\n")})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := r.Fields["ops"].(OpsReport)
	return r, o
}

func TestUnitedTakeoffData(t *testing.T) {
	r, o := united(t, "QUNDCULUA~1TAKEOFF DATA\n** PART 01 OF 01 **\n************************\nT/O IAD 30/Y *T PROC*\n9500 FT\nA321-271 PW1133GA-JM\nTEMP 23C       ALT 30.21\nWIND 210/6 MAG\n     0KT HW 6KT XW\nTOCG/TRIM 27.0/-.--\n************************\nDRY\n************************\n       *FLAPS 2*\n      *BLEEDS ON*\n     *ANTI-ICE OFF*\n  ASSMD WT:  186.5\n  ASSMD TMP: 45C\n************************\nREDUCED EPR -.--\nN1     *HW*  V1  VR  V2\n87.1   0KT   148 148 150\n************************\nMAX EPR -.--\nN1    TOG    V1  VR  V2\n88.7  186.5  132 143 148\n************************\nTHR RED 1112 (800 AFE)\nACC ALT 1112 (800 AFE)\n************************\nMTOG 206.1    VR MAX 155\n************************\nMEL/CDL:\n4490 STARLINK ANTENNA\nINSTALLATION\n************************\n")
	if r.Kind != KindOps || o.Airline != "United" || o.Event != "take-off data" || o.Origin != "KIAD" || o.Runway != "30" || o.Info["vspeeds"] != "148/148/150" || o.Info["flaps"] != "2" || o.Info["surface"] != "dry" {
		t.Fatalf("%v %+v", r.Kind, o)
	}
	if want := "United take-off data at KIAD: runway 30, V1/VR/V2 148/148/150, 1 open MEL/CDL items"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
	for _, want := range []string{"Temperature 23°C, altimeter 30.21", "Reduced thrust: assumed weight 186,500 lb, assumed temperature 45°C", "Full thrust at 186,500 lb: N1 88.7, V1 132, VR 143, V2 148", "Thrust reduction at 1112 ft (800 ft above the field)", "4490 STARLINK ANTENNA INSTALLATION"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, r.Text)
		}
	}
}

func TestUnitedFinalWeightsAndRelease(t *testing.T) {
	r, o := united(t, "QUNDCULUA~1FINAL WEIGHTS\nUA2225/05 KPHL KSFO\nSENT: 13:58:12Z\n************************\nTOG      168616  SEQ:01\nTOW CHG  + 1437\n\nZFW      135916\n\nCG         22.6\n************************\nSOB 154\nPSGRS  16/130     -146-\n      (16/150)   LAP 02\nCREW   02/04    KIDS 03\nFD JUMPSEAT-0\n")
	if o.Event != "final weights" || o.Flight != "UA2225" || o.Origin != "KPHL" || o.Dest != "KSFO" || o.Info["souls"] != "154" || o.Info["pax"] != "146" || o.Info["sent"] != "13:58:12" {
		t.Fatalf("%+v", o)
	}
	if want := "United 2225 final weights KPHL→KSFO: take-off weight 168,616 lb, 154 on board"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
	if !strings.Contains(r.Text, "Zero fuel weight 135,916 lb") || !strings.Contains(r.Text, "146 passengers (16 and 130 by cabin)") {
		t.Errorf("text %s", r.Text)
	}

	r, o = united(t, "QUNDCULUA~1DISPATCH RELEASE\nUA2225/05 KPHL KSFO\nSENT: 13:37:44Z\n\nDISPATCH RELEASE\n\nUAL2225(2225) - 05NOV26\nKPHL/PHL     KSFO/SFO\n\nN17362/7362 B38M\n-----------------------\nRELEASED IFR TO KSFO\nCHI90\n\nDEPARTURE ALTN: N/A\nDESTINATION ALTN: N/A\n\nMIN T/O FUEL:29121\n\nST DEP 1410\n")
	if o.Event != "dispatch release" || o.Reg != "N17362" || o.Info["type"] != "B38M" || o.Info["day"] != "05" || o.Info["min_fuel"] != "29,121 lb" || r.Identity.Tail != "N17362" {
		t.Fatalf("%+v tail %q", o, r.Identity.Tail)
	}
	if want := "United 2225 dispatch release KPHL→KSFO: minimum take-off fuel 29,121 lb"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
}

func TestUnitedHowgozitGateAndTurbulence(t *testing.T) {
	r, o := united(t, "QUNDCULUA~1HOWGOZIT\n\t** PART 01 OF 01 **\n\tHOWGOZIT 0804-04 HND\n\tCI: 275       RLS: 02 \n\tHND 0811/0855    2058A\n\tINUBO    0913 29 1951-\n\tGULBO    0918 31 1930-\n\tIAD 2047/2054     188\n\t--SCHEDULED ARRIVAL--\n\t    2033/2040 (00.14L)\n\t--DISPATCH PLAN--\n")
	if o.Event != "howgozit" || o.Flight != "UA804" || o.Origin != "RJTT" || o.Dest != "KIAD" || o.Info["cost_index"] != "275" || o.Info["waypoints"] != "2" {
		t.Fatalf("%+v", o)
	}
	if want := "United 804 howgozit RJTT→KIAD: arrival 20:47Z"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
	if !strings.Contains(r.Text, "the plan is 0h14m late") {
		t.Errorf("text %s", r.Text)
	}

	r, o = united(t, "QUNDCULUA~1GATE ASSIGN\n\tUA0889/02 ZBAA KSFO   \n\tSENT: 18:44:04Z \n\tGATE G5    AVAIL 2205\n\tRAMP FREQ  119.225\n\tOPS FREQ   129.100\n\tGROUND PWR AVAIL -YES-\n\tPLANNED GROUND TIME \n\tIS 19:37\n\tNEXT FLT UA0875/03\n\tKSFO RJTT ETD 1820Z\n")
	if o.Event != "gate assignment" || o.Info["gate"] != "G5" || o.Info["next"] != "UA875" || !strings.Contains(r.Text, "Next flight UA875 KSFO→RJTT, departing 1820Z") {
		t.Fatalf("%+v\n%s", o, r.Text)
	}

	r, o = united(t, "QUNDCULUA~1TURB SIGMET\n\tUAL876-31 RJTT KSFO\n\tYOUR FLIGHT ENTERS A\n\tTURBULENCE SIGMET AREA\n\tAT 1329Z FL370\n\tTYPE CAT\n\tSEVERITY MOD\n\tALTITUDE FL310 - FL390\n")
	if want := "United 876 turbulence SIGMET RJTT→KSFO: moderate turbulence"; r.Summary != want || !strings.Contains(r.Text, "Altitudes FL310 to FL390") {
		t.Errorf("summary %q\n%s", r.Summary, r.Text)
	}
}

// A performance report gives its position and some numbers on a line of its own or run together: a long one reaches us whole.
func TestPerformanceReportNumbers(t *testing.T) {
	text := "APM    1 N24993         UAL182  KDENEDDF211026232013,\r\n,70000021,.843,,,274.5,,,-13.07,,,37001,,,485547,,,6483,6485,,,\r\n 92.26, 92.25,,,  1.069,545.7,,,\r\n 61.5, 60.9, 81.5, 80.9,,,0,0,1,,,,,, 43.7566,,,\r\n -92.7391,,,  71.0,,,  4.7,,,  75.4,,,  -0.001, 0.006,    0,,,\r\n 6.673,  15, 55543,36600,36900,,,,,,10553,10584,,,  4.3,\r\n"
	r, err := Decode(Message{Label: "H1", Text: text})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := r.Fields["ops"].(OpsReport)
	if o.Event != "APM" || r.Pos == nil || r.Pos.Lat != 43.7566 || r.Pos.Lon != -92.7391 || *r.Pos.AltFt != 37001 {
		t.Fatalf("%+v pos %+v", o, r.Pos)
	}
	if o.Info["mach"] != ".843" || o.Info["tat_c"] != "-13.07" || o.Info["gross_lb"] != "485547" || o.Info["fuel_flow"] != "6483/6485" {
		t.Errorf("info %+v", o.Info)
	}
	if !strings.Contains(r.Text, "Mach .843, total air temperature -13.07°C") || !strings.Contains(r.Text, "Gross weight 485,547 lb") {
		t.Errorf("text %q", r.Text)
	}

	// Alaska's, with the longitude on the same line
	r, _ = Decode(Message{Label: "H1", Text: "B03A     \r\nAPM     N956AK          ASA421  KDENKSEA061126035556,737789     ,BCG3BL2000003,.614,,370.6,249.2,,,-14.87,,,26007,,,151382,,,2361,2342,,, 75.36, 75.38,,, 5.3,321.8,,,0,0,,0,0,,1,1,0,0,0,0,,, 46.8115,,,-120.6907,,, -25.01,,,  4.1,,,"})
	o, _ = r.Fields["ops"].(OpsReport)
	if r.Pos == nil || r.Pos.Lat != 46.8115 || r.Pos.Lon != -120.6907 || o.Info["mach"] != ".614" || o.Info["gross_lb"] != "151382" {
		t.Errorf("alaska: %+v %+v", o, r.Pos)
	}
}
