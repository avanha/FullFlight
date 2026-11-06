package decoder

import (
	"testing"
)

// Real messages from B-16783, an EVA Air 777.

// A registration of seven characters fills the aircraft address, so there is no padding dot behind the application: .AT1B-16783.
func TestSevenCharacterRegistrationsDecode(t *testing.T) {
	r, _ := Decode(Message{Label: "BA", Text: "/USADCXA.AT1B-167836D193E88005BF4"})
	if r.Kind != KindCPDLC || r.Identity.Tail != "B-16783" {
		t.Errorf("CPDLC: %v %q %q", r.Kind, r.Identity.Tail, r.Summary)
	}
	r, _ = Decode(Message{Label: "B6", Text: "F63ABR0619/OAKODYA.ADSB-1678307161DF401A9C8CA1E0A9D0D160B64000008CA0072149F4BC71C88CA000E5831007FFC0F5AF99D7FFCCC53"})
	if r.Kind != KindADSC || r.Pos == nil || r.Identity.Tail != "B-16783" {
		t.Errorf("ADS-C: %v %+v %q", r.Kind, r.Pos, r.Summary)
	}
}
