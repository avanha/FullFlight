package flightplan

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func names(p *Plan) []string {
	var n []string
	for _, f := range p.Fixes {
		n = append(n, f.Name)
	}
	return n
}

func TestUPSPlan(t *testing.T) {
	p, ok := ParsePlan("FPN/FNUPS58/RI:DA:PHNL:AA:VHHH:F:LILIA..N25000W170000..N25000E180000..TUNTO..IGURU.Q13..PICHU.J7..HCN,N21557E120506.G581..ELATO.V522..ABBEY,N22162E1145541A98")
	if !ok || p.Callsign != "UPS58" || p.From != "PHNL" || p.To != "VHHH" {
		t.Fatalf("%+v %v", p, ok)
	}
	want := []string{"LILIA", "N25000W170000", "N25000E180000", "TUNTO", "IGURU", "PICHU", "HCN", "ELATO", "ABBEY"}
	if !reflect.DeepEqual(names(p), want) {
		t.Fatalf("fixes %v", names(p))
	}
	f := p.Fixes
	if !f[1].HasPos || f[1].Lat != 25 || f[1].Lon != -170 || f[2].Lon != 180 {
		t.Errorf("coordinates: %+v %+v", f[1], f[2])
	}
	if f[4].Via != "Q13" || f[5].Via != "J7" || f[6].Via != "G581" || f[3].Via != "" {
		t.Errorf("airways: %+v", f)
	}
	// name with coordinates: the coordinates win, and the trailing checksum is not part of them
	last := f[8]
	if !last.HasPos || math.Abs(last.Lat-(22+16.2/60)) > 1e-9 || math.Abs(last.Lon-(114+55.4/60)) > 1e-9 {
		t.Errorf("ABBEY = %+v", last)
	}
}

func TestMultiLineAndAirwayForms(t *testing.T) {
	// as sent for the Asiana flight: wrapped lines with CR LF, and an airway followed by ".."
	text := "FPN/RP:DA:KLAX:AA:RKSI:F:MCKEY..SNS..OAK..FOT..BOXER..FULMR.\r\n.FASEL..N51W140..N55W150..PDN.G469..NYMPH.R220..NODAN.R217..\r\nASTER.Y514..SDE.Y512..GTC.Y142..SAMON.Y14..SUGNO.Y16..SAPRA.\r\nY685..GUKDO..RKSIC189\r\n"
	p, ok := ParsePlan(text)
	if !ok || p.Callsign != "" || p.From != "KLAX" || p.To != "RKSI" {
		t.Fatalf("%+v %v", p, ok)
	}
	want := []string{"MCKEY", "SNS", "OAK", "FOT", "BOXER", "FULMR", "FASEL", "N51W140", "N55W150", "PDN", "NYMPH", "NODAN", "ASTER", "SDE", "GTC", "SAMON", "SUGNO", "SAPRA", "GUKDO", "RKSI"}
	if !reflect.DeepEqual(names(p), want) {
		t.Fatalf("fixes %v", names(p))
	}
	for _, f := range p.Fixes {
		if f.Name == "NYMPH" && f.Via != "R220" || f.Name == "PDN" && f.Via != "G469" {
			t.Errorf("airway lost: %+v", f)
		}
	}
	// "PEQ,coords.Q4.FUSCO..DILLO": an airway between two fixes joined by single dots
	p, ok = ParsePlan("FPN/FNDAL946/RP:DA:KLAX:AA:KAUS:F:CNERY,N33371W115349..PEQ,N31282W103345.Q4.FUSCO..DILLO,N30478W098472/FNDAL946/SN660358")
	if !ok || !reflect.DeepEqual(names(p), []string{"CNERY", "PEQ", "FUSCO", "DILLO"}) || p.Fixes[1].Via != "Q4" || p.Callsign != "DAL946" {
		t.Fatalf("%+v %v", p, ok)
	}
	if lat := p.Fixes[0].Lat; math.Abs(lat-(33+37.1/60)) > 1e-9 {
		t.Errorf("lat %v", lat)
	}
}

func TestOtherForms(t *testing.T) {
	// a prefix before FPN, a company route, procedures and an approach: only the airports and F: routes count
	p, ok := ParsePlan("- #MDFPN/FNUPS81/RI:DA:ZSPD:AA:PANC:F:LAMEN.A593..SADLI.Y590..SAMON,N36146E134302.Y142..PORGE.A590..HAMND27A0")
	if !ok || p.Callsign != "UPS81" || p.To != "PANC" || len(p.Fixes) != 5 || p.Fixes[4].Name != "HAMND" {
		t.Fatalf("%+v %v", p, ok)
	}
	p, ok = ParsePlan("FPN/FNAAL2034/RP:DA:KSAN:AA:KDFW:CR:KSANKDFW:R:27O(17C):D:ZZOOO4.GBN:A:BOOVE7.GEEKY:AP:ILS17C..GEEKY,N32168W099530/RA:DA:KDFW:AA:KAUS5096")
	if !ok || p.From != "KSAN" || p.To != "KDFW" || p.Callsign != "AAL2034" || len(p.Fixes) != 0 {
		t.Fatalf("a plan with no en-route part still names its airports: %+v %v", p, ok)
	}
	p, ok = ParsePlan("FPN/FNUAL1326/RP:DA:KAUS:AA:KIAH:F:ILEXY,N30212W0970486CDE")
	if !ok || len(p.Fixes) != 1 || !p.Fixes[0].HasPos || math.Abs(p.Fixes[0].Lon+(97+4.8/60)) > 1e-9 {
		t.Fatalf("%+v %v", p, ok)
	}
	for _, s := range []string{"", "REQFPN/CODD46", "RESFPN/AK,071", "/HDQDLUA.RESFPN/RJ,/GAHDQDLUA/TS221208,3010260825", "POSN34097W115415,MDLER,150025", "FPN/RP:DA:K:AA:KDFW"} {
		if p, ok := ParsePlan(s); ok {
			t.Errorf("%q parsed as %+v", s, p)
		}
	}
}

func TestParseCoord(t *testing.T) {
	cases := map[string][2]float64{
		"N51W140": {51, -140}, "S33E151": {-33, 151}, "N5130W14000": {51.5, -140}, "N25000E180000": {25, 180},
		"N43513W121533": {43 + 51.3/60, -(121 + 53.3/60)},
	}
	for in, want := range cases {
		lat, lon, ok := ParseCoord(in)
		if !ok || math.Abs(lat-want[0]) > 1e-9 || math.Abs(lon-want[1]) > 1e-9 {
			t.Errorf("%s = %v %v %v, want %v", in, lat, lon, ok, want)
		}
	}
	for _, in := range []string{"", "ABBEY", "N91W140", "N51W190", "N5E1", "N51W140X"} {
		if _, _, ok := ParseCoord(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestProgress(t *testing.T) {
	p, ok := ParseProgress("PRG/TS195622,301026/DTVHHH,07R,408,064946,048/FNUPS58FA3A")
	if !ok || p.Callsign != "UPS58" || p.To != "VHHH" || p.Runway != "07R" || p.ETA != "064946" || !p.At.Equal(time.Date(2026, 10, 30, 19, 56, 22, 0, time.UTC)) {
		t.Fatalf("%+v %v", p, ok)
	}
	p, ok = ParseProgress("PRG/TS040516,301026/DTKONT,26L,352,042015,042/FNUPS8132A7")
	if !ok || p.Callsign != "UPS81" || p.To != "KONT" {
		t.Errorf("%+v", p)
	}
	// Avianca's has no TS time, so there is no time of the report
	p, ok = ParseProgress("PRG/DTEGLL,09L,146,142154,046/FNAVA120C3DC")
	if !ok || p.Callsign != "AVA120" || p.To != "EGLL" || p.Runway != "09L" || p.ETA != "142154" || !p.At.IsZero() {
		t.Errorf("%+v %v", p, ok)
	}
	if _, ok := ParseProgress("PRG/XYZ"); ok {
		t.Error("not a progress report")
	}
	if _, ok := ParseProgress("FPN/RP:DA:KLAX:AA:KAUS:F:CNERY"); ok {
		t.Error("a plan is not a progress report")
	}
}

func TestResolve(t *testing.T) {
	p, _ := ParsePlan("FPN/RP:DA:KLAX:AA:RKSI:F:MCKEY..SNS..N51W140..PDN.G469..GUKDO..RKSIC189")
	db := map[string][][2]float64{
		"MCKEY": {{36.5, -121.9}},                 // central California
		"SNS":   {{36.66, -121.6}, {-23, 45}},     // a VOR, and a namesake on the other side of the world
		"PDN":   {{54.2, -167.7}, {40.1, -105.2}}, // Aleutians, and one in Colorado
		"GUKDO": {{36.9, 126.4}},
	}
	lookup := func(n string) [][2]float64 { return db[n] }
	airports := map[string][2]float64{"KLAX": {33.94, -118.41}, "RKSI": {37.46, 126.44}}
	apt := func(c string) (float64, float64, bool) { a, ok := airports[c]; return a[0], a[1], ok }
	pts, missing := Resolve(p, lookup, apt)
	var got []string
	for _, x := range pts {
		got = append(got, x.Name+"/"+x.Kind)
	}
	want := []string{"KLAX/airport", "MCKEY/fix", "SNS/fix", "N51W140/given", "PDN/fix", "GUKDO/fix", "RKSI/airport"}
	if !reflect.DeepEqual(got, want) || len(missing) != 0 {
		t.Fatalf("%v missing %v", got, missing)
	}
	if pts[2].Lat != 36.66 {
		t.Errorf("SNS should be the one near California, got %+v", pts[2])
	}
	if pts[4].Lat != 54.2 || pts[4].Via != "G469" {
		t.Errorf("PDN should be the Aleutian one: %+v", pts[4])
	}
	// a name we do not have is reported, and the route carries on
	delete(db, "SNS")
	pts, missing = Resolve(p, lookup, apt)
	if len(pts) != 6 || !reflect.DeepEqual(missing, []string{"SNS"}) {
		t.Errorf("%d points, missing %v", len(pts), missing)
	}
	// a name with only a far-away candidate is not used
	db["MCKEY"] = [][2]float64{{-33, 151}}
	if _, missing = Resolve(p, lookup, apt); missing[0] != "MCKEY" {
		t.Errorf("missing %v", missing)
	}
}
