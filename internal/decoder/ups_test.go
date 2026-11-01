package decoder

import "testing"

// The three UPS route messages, as received from N575UP leaving Anchorage for Louisville on 31 Oct 2026.
const (
	ups3JSample = "010111 31ANC SDF "
	ups3NSample = "PDC UP0065/31 ANC-SDF\r\n\t----ATC CLEARANCE----\r\n\tCLEARED AS FILED\r\n\t \r\n\t CLEARED ANCH1\r\n\t DEPARTURE \r\n\tSQWK: 7223\r\n" +
		"\t----FILED FLT PLAN----\r\n\tANC JOH J501 YAK DEEJA\r\n\tMITOM/ SDF@RVR 075 TCAS\r\n\tCARG*\r\n\tPROPOSED ETD: 0109Z\r\n\tREQUESTED ALT: 330\r\n\t---END OF CLEARANCE---"
	ups41Sample = "0065 /1031/ANC /SDF /0109/0549/13/10//0554744   /7330622   /          /////////////0/1/0/1/0/0/0/0//"
)

func TestUPSDepartureReport(t *testing.T) {
	r, err := Decode(Message{Label: "3J", Dir: Downlink, Text: ups3JSample})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := r.Fields["departure"].(UPSNotice)
	if r.Kind != KindDeparture || !ok || n.Origin != "PANC" || n.Dest != "KSDF" || n.Sent != "010111" || n.Day != 31 {
		t.Fatalf("%s %+v", r.Kind, r.Fields)
	}
	if r.Summary != "UPS departure report PANC→KSDF" {
		t.Errorf("summary %q", r.Summary)
	}
}

func TestUPSClearance(t *testing.T) {
	r, err := Decode(Message{Label: "3N", Dir: Uplink, Text: ups3NSample})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := r.Fields["departure"].(UPSNotice)
	if r.Kind != KindDeparture || !ok || n.Origin != "PANC" || n.Dest != "KSDF" || n.Airline != "UP" || n.Flight != "65" || n.ETD != "0109" || n.Day != 31 {
		t.Fatalf("%s %+v", r.Kind, r.Fields)
	}
	if r.Identity.Flight != "UP65" || r.Identity.FlightID != "UP0065" {
		t.Errorf("the clearance still names the flight: %+v", r.Identity)
	}
	if r.Summary != "UPS clearance UP65 PANC→KSDF, planned departure 01:09Z" {
		t.Errorf("summary %q", r.Summary)
	}
}

func TestUPSSchedule(t *testing.T) {
	r, err := Decode(Message{Label: "41", Dir: Uplink, Text: ups41Sample})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := r.Fields["departure"].(UPSNotice)
	if r.Kind != KindDeparture || !ok || n.Origin != "PANC" || n.Dest != "KSDF" || n.Flight != "65" || n.Date != "1031" || n.ETD != "0109" || n.ETA != "0549" {
		t.Fatalf("%s %+v", r.Kind, r.Fields)
	}
	if r.Summary != "UPS schedule 65 PANC→KSDF, planned 01:09Z to 05:49Z" {
		t.Errorf("summary %q", r.Summary)
	}
}

// Only the right label, with text of the right shape, is one of these; an airport the list lacks stays as sent.
func TestUPSNotUPS(t *testing.T) {
	for _, c := range []struct{ label, text string }{
		{"3J", "ANC SDF"}, {"3J", "010111 31ANC"}, {"3N", "PDC UP0065 ANC-SDF"}, {"41", "0065 ANC SDF"},
		{"B9", ups3JSample}, {"3J", ups41Sample}, {"", ups3JSample},
	} {
		if _, ok := ParseUPS(c.label, c.text); ok {
			t.Errorf("%s %q should not parse", c.label, c.text)
		}
	}
	if n, ok := ParseUPS("3J", "010111 31ZZZ QQQ"); !ok || n.Origin != "ZZZ" || n.Dest != "QQQ" {
		t.Errorf("unknown airports are kept as sent: %+v %v", n, ok)
	}
}
