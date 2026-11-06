package decoder

import (
	"testing"
	"time"
)

// Real messages from N956AK, an Alaska Airlines 737, checked against its ADS-B track and its landing.

func alaska(t *testing.T, label, text string) (Result, OpsReport) {
	t.Helper()
	r, err := Decode(Message{Label: label, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := r.Fields["ops"].(OpsReport)
	return r, o
}

func TestAlaskaMovementReports(t *testing.T) {
	r, o := alaska(t, "80", "1101 OFFRP  1388/31 KBUR/KSEA .N956AK\r\n/OUT 1744/OFF 1804/FOB 0172/ETA 2004/CAB 1739/OIL 1748/AFT     /CARGO 1728/SRVC     ")
	if r.Kind != KindOps || o.Flight != "AS1388" || o.Origin != "KBUR" || o.Dest != "KSEA" || r.Identity.Tail != "N956AK" || o.Out != "1744" || o.Off != "1804" || o.ETA != "2004" {
		t.Errorf("OFFRP: %v %+v %q", r.Kind, o, r.Summary)
	}
	r, o = alaska(t, "80", "1201 ONRP   0670/01 KLAX/KPDX .N956AK\r\n/ON  2016/FOB 0087")
	if r.Kind != KindOps || o.Flight != "AS670" || o.Origin != "KLAX" || o.Dest != "KPDX" || o.On != "2016" {
		t.Errorf("ONRP: %v %+v", r.Kind, o)
	}
	r, o = alaska(t, "80", "3701 INRANG 1614/31 KSEA/KLAX .N956AK\r\n/ETA 0154")
	if r.Kind != KindOps || o.Flight != "AS1614" || o.Origin != "KSEA" || o.Dest != "KLAX" || o.ETA != "0154" {
		t.Errorf("INRANG: %v %+v", r.Kind, o)
	}
	r, o = alaska(t, "80", "1701 FAROUT 0649/01 KPDX/KBUR .N956AK\r\n/OUT 2307/FAROUT 230947/FOB 0156")
	if r.Kind != KindOps || o.Flight != "AS649" || o.Origin != "KPDX" || o.Dest != "KBUR" || o.Out != "2307" || o.Off != "" {
		t.Errorf("FAROUT: %v %+v", r.Kind, o)
	}
}

func TestAlaskaTakeoffAndPerformanceReports(t *testing.T) {
	r, o := alaska(t, "H1", "B44B\r\nN956AK    21388KBURKSEATO311026180429\r\n2201 61560A91460A915BCG3B-L200-0003\r\n   758 1669119411850183  263  243153040\r\nPRI   -3OFFOFFOFFAUTOAUTOCLOSEDON ON \r\n   770 983 9510150  330  345\r\n 9278 9281 928")
	if r.Kind != KindOps || o.Flight != "AS1388" || o.Origin != "KBUR" || o.Dest != "KSEA" || o.OffAt != "2026-10-31T18:04:29Z" || r.Identity.Tail != "N956AK" {
		t.Errorf("takeoff: %v %+v", r.Kind, o)
	}
	r, o = alaska(t, "H1", "B03A     \r\nAPM     N956AK          ASA557  PHNLKLAX301026021414,737789     ,BCG3BL2000003,.791,,467.5,268.7,,,-14.43,,,35016,,,154922,,,2569,2546,,, 86.86, 86.80,,, 1.9,519.2,,,0,0,,0,0,,0,0,0,0,0,0,,, 22.1")
	if r.Kind != KindOps || o.Event != "APM" || o.Flight != "AS557" || o.Callsign != "ASA557" || o.Origin != "PHNL" || o.Dest != "KLAX" || o.At != "2026-10-30T02:14:14Z" || o.Airline != "Alaska" {
		t.Errorf("performance report: %v %+v", r.Kind, o)
	}
}

// The ABS position report: the longitude and the time and altitude run together, and the report was delivered a day late.
func TestAlaskaPositionReport(t *testing.T) {
	r, o := alaska(t, "H1", "ABS001DA_N956AK  KSEAKMCO271\r\n 46962-1197061642030855-483278032XA0/9W0190230000--\r\n")
	if r.Kind != KindOps || o.Origin != "KSEA" || o.Dest != "KMCO" || r.Identity.Tail != "N956AK" || r.Pos == nil || r.Pos.Lat != 46.962 || r.Pos.Lon != -119.706 || *r.Pos.AltFt != 30855 {
		t.Fatalf("%v %+v %+v", r.Kind, o, r.Pos)
	}
	if w := r.Wx; w == nil || *w.TempC != -48 || *w.WindDirDeg != 278 || *w.WindKt != 32 {
		t.Errorf("weather %+v", w)
	}
	if at := r.Pos.At(time.Date(2026, 10, 30, 5, 51, 30, 0, time.UTC)); !at.Equal(time.Date(2026, 10, 29, 16, 42, 0, 0, time.UTC)) {
		t.Errorf("placed at %v", at)
	}
}
