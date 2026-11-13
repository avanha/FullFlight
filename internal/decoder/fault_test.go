package decoder

import "testing"

// Airbus failure reports (British Airways' samples): the system, the message that went with it and the ATA chapter.
func TestFaultReports(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"FLR/PNRC12860AA08/FR26102622030023733406SDF1(101RH)-DIR1(101RH)\r\n/IDSMOKE\r\n", "Fault report: SDF1(101RH)-DIR1(101RH) (SMOKE), ATA 23-73-34, at 2026-10-26 22:03Z"},
		{"FLR/FR26110113190029114106SDAC1:G HYD QTY     IND 1000GQ\r\n/IDECAM 1\r\n", "Fault report: SDAC1:G HYD QTY IND 1000GQ (ECAM 1), ATA 29-11-41, at 2026-11-01 13:19Z"},
	} {
		r, _ := Decode(Message{Label: "H1", Text: c.text})
		if r.Kind != KindFault || r.Summary != c.want {
			t.Errorf("%v %q", r.Kind, r.Summary)
		}
	}
}
