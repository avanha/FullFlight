package decoder

import (
	"strings"
	"testing"
)

// Real messages from N501DN, a Delta A350, on its flights of 28 October and 2 November (the fueler's name left out).

func delta(t *testing.T, label, text string) (Result, OpsReport) {
	t.Helper()
	r, err := Decode(Message{Label: label, Dir: Uplink, Text: strings.ReplaceAll(text, "\n", "\r\n")})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := r.Fields["ops"].(OpsReport)
	return r, o
}

func TestDeltaPrePushbackNamesTheOpenMELItems(t *testing.T) {
	r, o := delta(t, "RA", "50 PRE-PUSHBACK MSG\nFLT 0782/02NOV\nA/C 3501 LAX-ATL RLS 1\n \n***EFSR***\n \nTOTAL 66.6 TAXI 1.5 \n \nMEL: C57-00-23  \n     C27-00-20  \n     S22-00-00A \n     N25-21-35  \n \nWDR RWYS:\n25R-F               \n24L-E8              \nIF RWY CHG IS REQD: \nSEND ACARS T/O PERF\n")
	if r.Kind != KindOps || o.Event != "pre-pushback" || o.Flight != "DL782" || o.Callsign != "DAL782" || o.Origin != "KLAX" || o.Dest != "KATL" || o.Info["day"] != "02" {
		t.Fatalf("%v %+v %q", r.Kind, o, r.Summary)
	}
	if o.Info["mel"] != "C57-00-23 C27-00-20 S22-00-00A N25-21-35" || o.Info["fuel"] != "66.6" || o.Info["runways"] != "25R-F 24L-E8" || o.Info["ship"] != "3501" {
		t.Errorf("%+v", o.Info)
	}
	if want := "Delta 782 pre-pushback message KLAX→KATL: fuel 66,600 lb, 4 open MEL items"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
	if !strings.Contains(r.Text, "Open deferred defects (MEL): C57-00-23, C27-00-20, S22-00-00A, N25-21-35") {
		t.Errorf("text %q", r.Text)
	}
}

func TestDeltaFuelServiceRecord(t *testing.T) {
	r, o := delta(t, "RA", "50 ACARS FUEL SVC RCD\x07\nFUEL DATA RECEIVED FROM EFSR\nFLT 0782/02NOV SHIP 3501 LAX-ATL\nEFSR DATE/TIME 02NOV 1555Z\n \nFUEL BOARDED IN GALLONS -   04730\nTOTAL FUEL OUT IN POUNDS - 066600\nFUEL CAPS PRESENT    \nFUEL TYPE - JETA           \n     -----  FUEL  BY  TANK  -----\nTK/ID TK BEGN      FUEL ADD   TOTAL FUEL\n      FUEL WT      TO TANK     THIS TANK\nLM     017600       015700       033300\nRM     017600       015700       033300\nCA     000000       000000       000000\n")
	if r.Kind != KindOps || o.Event != "fuel" || o.Flight != "DL782" || o.Origin != "KLAX" || o.Dest != "KATL" || o.Info["fuel_gal"] != "4730" || o.Info["fuel_lb"] != "66,600" {
		t.Fatalf("%v %+v %q", r.Kind, o, r.Summary)
	}
	if !strings.Contains(r.Text, "Tank LM: 17,600 lb before, 15,700 lb added, 33,300 lb after") {
		t.Errorf("text %q", r.Text)
	}
}

const deltaWDRText = `50 ACARS WDR UPDATE
FLT 782/2NOV SHIP 3501 LAX-ATL RLS 1 
WDR DATE/TIME 02NOV 1602Z
SOB/291 
CARGO 2.49/20.82/22.09/1.41/1.01  
 
FUEL TOTAL 66.6 TAXI 1.5 
TANK 33.3/0/33.3 
RMWT 485.8 ZFWT 419.2 ZFWCG 29.3
TOWT 484.4 TOCG 30.3
FLAP/STAB 01/05.3 02/05.3 03/05.3 
 
WX 02805KT  17C A30.15
 
*25R-F*             11134FT 04TW 03XW   
  T/F  CLIMB RATOW WIND V1 VR V2        
DDR/01 504.8 487.3 TW04 49 50 55 D24 X25
 
*24L-E8*             9133FT 04TW 03XW   
 
RMKS-NOTES-MEL
C57-00-23 
C27-00-20 
S22-00-00A
N25-21-35 
NO LIVE
 
FLT 782/2NOV SHIP 3501 LAX-ATL RLS 1 
END ACARS WDR
`

func TestDeltaWeightAndDataRelease(t *testing.T) {
	r, o := delta(t, "RA", deltaWDRText)
	if r.Kind != KindOps || o.Event != "WDR" || o.Flight != "DL782" || o.Runway != "25R-F" || o.Info["on_board"] != "291" || o.Info["tow"] != "484.4" || o.Info["zfw"] != "419.2" {
		t.Fatalf("%v %+v %q", r.Kind, o, r.Summary)
	}
	for _, want := range []string{"291 people on board", "Take-off weight 484,400 lb (centre of gravity 30.3%)", "Take-off runway 25R-F, 11,134 ft, 4 kt tailwind, 3 kt crosswind",
		"Take-off runway 24L-E8, 9,133 ft", "wind 028° at 5 kt, 17°C, altimeter 30.15", "Open deferred defects (MEL): C57-00-23, C27-00-20, S22-00-00A, N25-21-35"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, r.Text)
		}
	}
	// the first block of a long message is cut anywhere: what is there is read
	r, o = delta(t, "RA", "42 WIDE WDR 782/02 1602Z\nFLT 782/2NOV SHIP 3501 LAX-ATL RLS 1      *25R-F*             11134FT 04TW 03XW   \nWDR DATE/TIME 02NOV 1602Z                   T/F  CLIMB RATOW WIND V1 VR V2        \n")
	if r.Kind != KindOps || o.Event != "WDR" || o.Origin != "KLAX" || o.Runway != "25R-F" {
		t.Errorf("a cut message: %v %+v", r.Kind, o)
	}
}

func TestDeltaDispatchReleaseNamesTheAircraftAndTheRoute(t *testing.T) {
	r, o := delta(t, "RA", `40 FLT NBR DAL87   RLS 1                     ZFW  385.9           ZFWCG  25.0
KLAX-VHHH  ALT VMMC                     BLOCK  221.5
REDSP AT NIRAI / RQD 40.5                TAXI    1.7           PAX NBR 213
CRZ FL340   CRZ TEMP -54                 TRIP  197.8  14:16         CI  12
                                       RTE RSV   0.0
              TROPO FL399                  ALT   4.8  00:13
                                         FINAL        00:45       TOW 605.7
CI 12                                  MIN T/O 218.1               LW 407.9
TAXI OUT/IN 28/05                                               EXTRA   9.2

FLIGHT PLAN
KLAX SUMMR2 MCKEY DCT LIBBO DCT BRINY DCT ALCOA DCT BUTEN DCT BOSKE DCT 
38N140W DCT 38N150W DCT ELATO V522 
ABBEY DCT VHHH

FL STEPS
KLAX/FL340 BOSKE/FL360 37N160W/FL380 29N160E/FL400 

DIST 6752 NM

ETOPS
KSFO N38054W146579 PHNL N36422W161101 PMDY N29046E160338 RJTT N26109E127360 VHHH

FIX LIST
KLAX DARRK SUMMR

DAL87/28OCT KLAX-VHHH RLS 1 N501DN (ACARS FLT NO 0087)
`)
	if r.Kind != KindOps || o.Event != "dispatch release" || o.Flight != "DL87" || o.Origin != "KLAX" || o.Dest != "VHHH" || o.Reg != "N501DN" || r.Identity.Tail != "N501DN" || o.Info["day"] != "28" {
		t.Fatalf("%v %+v %q", r.Kind, o, r.Summary)
	}
	if o.Info["pax"] != "213" || o.Info["tow"] != "605.7" || o.Info["lw"] != "407.9" || o.Info["block"] != "221.5" || o.Info["alternate"] != "VMMC" || o.Info["dist_nm"] != "6752" ||
		o.Info["etops"] != "KSFO PHNL PMDY RJTT VHHH" || !strings.HasPrefix(o.Info["route"], "KLAX SUMMR2 MCKEY DCT LIBBO") || !strings.HasSuffix(o.Info["route"], "ABBEY DCT VHHH") {
		t.Errorf("%+v", o.Info)
	}
	if want := "Delta 87 dispatch release KLAX→VHHH: 213 passengers, take-off 605,700 lb"; r.Summary != want {
		t.Errorf("summary %q, want %q", r.Summary, want)
	}
}

func TestDeltaCrewVerificationAndOtherAirlinesAreLeftAlone(t *testing.T) {
	for _, text := range []string{"QUNDCULUA~1RLS VERIFICATION\nRLS 01\nPO EMP NAME   EMP NO FFD\n-- ---------- ------ ---\nCA PM HAMILTO 252213 OK \n", "42 RLS NBR 01 OK\n0946-25 LAX AUS\n"} {
		if r, _ := delta(t, "RA", text); r.Kind == KindOps {
			t.Errorf("%q was read as an operations report", text)
		}
	}
}

func TestDeltaPrePushbackBeforeTheFuelIsCounted(t *testing.T) {
	r, o := delta(t, "25", "42 PRE-PUSHBACK MSG\nFLT 2947/24OCT\nA/C 3723 PDX-LAX RLS 1\n \n***FUEL CLOSEOUT PENDING***\nDO NOT PUSHBACK WITHOUT\nAN EFSR OR PAPER FSR\nTOTAL 0.0 TAXI 0.0 \n \nMEL: S53-00-03  \n     S02-10-00  \n     S34-00-01  \n")
	if o.Info["fuel"] != "" || o.Info["mel"] != "S53-00-03 S02-10-00 S34-00-01" || o.Dest != "KLAX" || !strings.Contains(r.Text, "Fuel closeout pending") {
		t.Errorf("%+v %q", o, r.Text)
	}
}
