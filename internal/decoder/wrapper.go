package decoder

import "regexp"

// UPS 747s send their ACARS over satellite with a header of their own in front of the usual message, so that
// the ARINC 622 text (ADS-C, CPDLC, AFN) or the *POS report sits behind something that is not a message number
// and flight that SplitPrefix knows:
//
//	F21AUP0081#M1B/B6 ANCATYA.ADS.N575UP0727339BF5...      a downlink: message number, flight, "#M1B", the label
//	F27AUP0081#M1B/BA ANCXFXA.CC1.N575UP6B0332144910D8EE   (B6 ADS-C, BA CPDLC, B0 AFN)
//	D67AUP0081#DFB*POS                                     a position report: message number, flight, "#DFB"
//	10291918N5643W17618310219040859M3620828
//	- #MD/A6 OAKODYA.ADS.N575UP07020B000D010E010F01BB58    an uplink: "- #MD", the label (A6, AA, A0)
//
// Read from samples, not a specification. The label inside is the one the message would have had on VDL2, and it
// says which way it went (A uplink, B downlink).
var (
	wrapLabelRe = regexp.MustCompile(`(?s)^(?:([A-Z0-9]{4})([A-Z0-9]{2}\d{4}))?(?:- ?)?#M[A-Z0-9]{1,2}/([A-Z0-9]{2}) (.+)$`)
	// China Cargo: a message number and sequence (O30ACK0624), "#10B" for some, then the report.
	wrapChinaRe = regexp.MustCompile(`(?s)^(?:([A-Z0-9]{3})ACK(\d{4}))?(?:#10B)?((?:POSA1|SB0122BA_).+)$`)
	wrapPosRe   = regexp.MustCompile(`(?s)^([A-Z0-9]{4})([A-Z0-9]{2}\d{4})#DFB(\*POS.*)$`)
)

// unwrapSatcom takes the satellite header off a message, returning what was inside, the label that was inside
// it (if the header carries one), and the 10 character message number and flight in front of it (if any).
func unwrapSatcom(text string) (inner, label, prefix string, ok bool) {
	if m := wrapLabelRe.FindStringSubmatch(text); m != nil {
		if m[1] != "" {
			prefix = m[1] + m[2]
		}
		return m[4], m[3], prefix, true
	}
	if m := wrapPosRe.FindStringSubmatch(text); m != nil {
		return m[3], "", m[1] + m[2], true
	}
	if m := wrapChinaRe.FindStringSubmatch(text); m != nil {
		return m[3], "", "", true
	}
	return text, "", "", false
}
