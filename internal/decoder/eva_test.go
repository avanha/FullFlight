package decoder

import (
	"testing"
)

func TestEVAMovementReports(t *testing.T) {
	if r, o := alaska(t, "12", "OFF01EVA619    /--310633KLAXRCTP\r\n06331088"); r.Kind != KindOps || o.Event != "OFF" || o.Flight != "BR619" || o.Origin != "KLAX" || o.Dest != "RCTP" || o.Off != "0633" || o.Info["day"] == "" {
		t.Errorf("OFF: %v %+v", r.Kind, o)
	}
	if r, o := alaska(t, "11", "M39ABR6312OUT01EVA6312   /--010148ZSAMRCTP\n\t0148 225-----"); r.Kind != KindOps || o.Event != "OUT" || o.Flight != "BR6312" || o.Origin != "ZSAM" || o.Dest != "RCTP" || o.Out != "0148" || o.Info["day"] == "" {
		t.Errorf("OUT: %v %+v", r.Kind, o)
	}
	if r, o := alaska(t, "13", "M36ABR6311ONN01EVA6311   /--312339ZSAMZSAM\n\t2339 226"); r.Kind != KindOps || o.Event != "ONN" || o.Flight != "BR6311" || o.Origin != "" || o.Dest != "ZSAM" || o.On != "2339" || o.Info["day"] == "" {
		t.Errorf("ONN: %v %+v", r.Kind, o)
	}
}

func TestFMSPositionBehindAMessageNumber(t *testing.T) {
	r, _ := Decode(Message{Label: "H1", Text: "F05ABR0620#M1BPOSN26555E124214,MOLKA,164056,310,MAMOD,164132,MUMBA,M30,29045,2031/TS164056,301026E5C0"})
	if r.Kind != KindFMS || r.Pos == nil || r.Fields["next_waypoint"] != "MAMOD" {
		t.Errorf("%v %+v %q", r.Kind, r.Pos, r.Summary)
	}
}
