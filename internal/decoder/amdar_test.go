package decoder

import (
	"testing"
	"time"
)

func TestWeatherObservations(t *testing.T) {
	at := func(r Result, recv time.Time) time.Time {
		if r.Pos == nil {
			t.Fatalf("no position: %q", r.Summary)
		}
		return r.Pos.At(recv)
	}
	{ // climb
		r, o := alaska(t, "H1", "A3M2312343KSEAKLAX0378P005094005G0009\r\nN47335W1221840455P003097006G0009\r\nN47349W1221840541P001128010B0009\r\nN47361W1221750631M001162012B0009\r\nN47366W1221560691M002189019B0009\r\n-0016\r\n")
		if r.Kind != KindOps || o.Event != "AMDAR" || r.Pos == nil || *r.Pos.AltFt != 6910 || *r.Wx.TempC != -2 {
			t.Fatalf("climb: %v %+v %+v", r.Kind, o, r.Pos)
		}
		if d := r.Pos.Lat - 47.61; d > 0.001 || d < -0.001 {
			t.Errorf("climb latitude %v", r.Pos.Lat)
		}
		if want := time.Date(2026, 10, 31, 23, 43, 0, 0, time.UTC); !at(r, time.Date(2026, 10, 31, 23, 43, 25, 0, time.UTC)).Equal(want) {
			t.Errorf("climb placed at %v, want %v", at(r, time.Date(2026, 10, 31, 23, 43, 25, 0, time.UTC)), want)
		}
	}
	{ // descent
		r, o := alaska(t, "H1", "D3M630PHNLKLAXN33025W11936005443050M042232068G0009\r\nN33033W1193102895M038232051G0009\r\nN33041W1192612778M035224045G0009\r\nN33049W1192132661M032210050G0009\r\nN33056W1191662545M030215050G0009\r\n-0016\r\n")
		if r.Kind != KindOps || o.Event != "AMDAR" || r.Pos == nil || *r.Pos.AltFt != 30500 || *r.Wx.TempC != -42 {
			t.Fatalf("descent: %v %+v %+v", r.Kind, o, r.Pos)
		}
		if d := r.Pos.Lat - 33.0417; d > 0.001 || d < -0.001 {
			t.Errorf("descent latitude %v", r.Pos.Lat)
		}
		if want := time.Date(2026, 10, 30, 5, 44, 0, 0, time.UTC); !at(r, time.Date(2026, 10, 30, 5, 46, 35, 0, time.UTC)).Equal(want) {
			t.Errorf("descent placed at %v, want %v", at(r, time.Date(2026, 10, 30, 5, 46, 35, 0, time.UTC)), want)
		}
	}
	{ // cruise
		r, o := alaska(t, "H1", "E301KSEAKLAXN46451W12208423523024M048201050G0009\r\nN46249W1220713263M047220020G0009\r\nN46027W1220223531M047229013G0009\r\nN45399W1215673808M047283008G0009\r\nN45171W1215133900M047264012G0009\r\n-0016\r\n")
		if r.Kind != KindOps || o.Event != "AMDAR" || r.Pos == nil || *r.Pos.AltFt != 30240 || *r.Wx.TempC != -48 {
			t.Fatalf("cruise: %v %+v %+v", r.Kind, o, r.Pos)
		}
		if d := r.Pos.Lat - 46.7517; d > 0.001 || d < -0.001 {
			t.Errorf("cruise latitude %v", r.Pos.Lat)
		}
		if want := time.Date(2026, 10, 31, 23, 52, 0, 0, time.UTC); !at(r, time.Date(2026, 11, 1, 0, 4, 51, 0, time.UTC)).Equal(want) {
			t.Errorf("cruise placed at %v, want %v", at(r, time.Date(2026, 11, 1, 0, 4, 51, 0, time.UTC)), want)
		}
	}
	{ // the last lines are filler where the observations ran out, and an altitude below zero
		r, o := alaska(t, "H1", "D3E701KLAXKPDXN45359W12238820150055P004262003G0009\r\nN45353W1223720012P006236002G0009\r\nN45348W122359-017P007265006G0009\r\nXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX\r\nXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX\r\n-0016\r\n")
		if r.Kind != KindOps || o.Event != "AMDAR" || o.Origin != "KLAX" || o.Dest != "KPDX" || r.Pos == nil || *r.Pos.AltFt != 550 {
			t.Errorf("%v %+v %+v", r.Kind, o, r.Pos)
		}
	}
}
