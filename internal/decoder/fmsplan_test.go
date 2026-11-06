package decoder

import (
	"github.com/avanha/fullflight/internal/flightplan"
	"strings"
	"testing"
)

// An FMS plan report comes in pages: the position report with the route and the forecast for each waypoint, then the rest of the list.
func TestFMSPlanReport(t *testing.T) {
	r, _ := Decode(Message{Label: "H1", Text: "POSN28258W082126,RDSOX,230650,181,JFRYS,230853,PATOY,M5,030010,344,391,387K,289K,73/RI:DA:KMCO:AA:KSEA(16R):D:RDSOX1.JFRYS:F:JFRYS..PATOY.Q116.JAWJA..MGM..MEMFS.Q116.SGF..ATIJA..JUPUL..FARMY..ONECI..KU06Q..MLP:A:GLASR3.MLP:F:VECTOR..DISCO..CIBUX:AP:ILS 16R.CIBUX:F:ZERLI/PR1782,217,340,344,,45,15,289038,M44,180,P0,P0/FHJFRYS,230853,290K,2065,341,M11,290050,409,Y,271,16.PATOY,231512,290K,2759,333,M28,293047,454,N,329,44.DEANR,231710,290K,2927,330,M32,292047,465,N,324,14.MICES,232314,274K,3400,323,M44,292042,465,N,324,44.JAWJA,232627,274K,3400,320,M44,290039,465,N,324,23.MGM,235112,274K,3400,297,M44,275041,465,N,313,178.MEMFS,002511,274K,3400,267,M44,237070,465,N,311,248.LUKKY,003853,274K,3400,255,M44,234067,465,N,311,101.ZAVEL,004028,274K,3400,254,M44,235065,465,N,/PG69,1"})
	p, _ := r.Fields["plan"].(*flightplan.Plan)
	if r.Kind != KindFMS || r.Pos == nil || p == nil || p.From != "KMCO" || p.To != "KSEA" || p.Runway != "16R" || len(p.Fixes) < 10 || p.Fixes[0].Name != "JFRYS" {
		t.Fatalf("%v %+v %q", r.Kind, p, r.Summary)
	}
	if !strings.Contains(r.Summary, "plan KMCO→KSEA runway 16R") {
		t.Errorf("summary %q", r.Summary)
	}
	names := map[string]int{}
	for _, f := range r.Fixes {
		names[f.Name]++
		if f.Name == "MGM" && (f.Role != "ahead" || f.Secs == nil || *f.Secs != 23*3600+51*60+12) {
			t.Errorf("MGM: %+v", f)
		}
	}
	if names["JFRYS"] != 1 || names["MGM"] != 1 || names["SGF"] != 0 || names["KSEA"] != 0 {
		t.Errorf("the waypoints in the next two hours, once each: %v", names)
	}
	r, _ = Decode(Message{Label: "H1", Text: "311,12.SGF,005406,273K,3400,242,M44,232050,464,N,310,102.ATIJA,012759,273K,3400,213,M44,284019,464,N,305,253.JUPUL,013951,273K,3400,203,M44,270024,464,N,297,88.FARMY,020637,273K,3400,181,M44,251053,464,N,301,195.ONECI,023329,273K,3400,158,M44,240086,464,N,306,192.KU06Q,025918,273K,3400,137,M44,245065,464,N,305,184.MLP,033429,273K,3400,108,M44,275036,464,Y,288,249.GEG,034556,274K,3400,99,M44,284045,465,Y,274,81.TEMPL,035653,274K,3400,90,M44,276053,465,Y,277,76.WITRO,040047,272K,3370,87,M57,278058,447,Y,276,27.ODAHL,040500,280K,2412,86,M38,281054,395,Y,276,27.GLASR,040746,280K,1857,85,M27,284050,363,Y,276,16.JAKSN,041143,250K,1200,85,M14,287053,295,Y,275,21.WOODI,041346,250K,1200,83,M14,289060,295,Y,222,9.HETHR,041727,250K,1015,81,M10,294068,275,Y,222,17.VECTOR,/PG69,2"})
	if r.Kind != KindProgress || r.Fields["fms_page"] != 2 || r.Pos != nil || !strings.Contains(r.Summary, "SGF 00:54Z to HETHR 04:17Z") {
		t.Errorf("page 2: %v %q", r.Kind, r.Summary)
	}
	r, _ = Decode(Message{Label: "H1", Text: ",,,,,294068,,Y,259.DISCO.CIBUX,042042,230K,600,81,M2,293065,245,Y,180,14.AGANE,042128,229K,500,80,P0,293067,241,Y,180,3.CELAK,042216,214K,400,80,P2,293070,222,Y,180,3.BUGNE,042256,201K,320,80,P4,293071,206,Y,180,3.FINKA,042406,179K,190,80,P6,294071,180,Y,180,4.RW16R,042536,176K,47,79,P9,000000,176,Y,180,4.900,,,90,,,000000,,Y,180,0.INTC,,,,,,000000,,Y,211,3.TECEE,,,200,,,000000,,Y,229,1.ZERLI,,,500,,,000000,,Y,229,12.ZERLI,,,500,,,000000,,Y,229,0.KSEA,,150K,43,,,000000,172,N,164,1/DTKSEA,16R,79,042536,73/PG69,3E317C"})
	g, _ := r.Fields["progress"].(*flightplan.Progress)
	if r.Kind != KindProgress || g == nil || g.To != "KSEA" || g.Runway != "16R" || g.ETA != "042536" {
		t.Errorf("page 3: %v %+v %q", r.Kind, g, r.Summary)
	}
}
