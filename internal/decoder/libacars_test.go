package decoder

import (
	"encoding/json"
	"testing"
)

func TestDecodeADSC(t *testing.T) {
	got, err := DecodeApps("B6", "/FUKJJYA.ADS..B-LRM07139FFB6420C94706B41F1604805B1928D888A01600ED5C613878A4A01712DD1B45BA49C3C8AC0E5B18EC40000F5B29A8C0000D13333355554946C44011C71B1C71C9C3C076C4", Downlink)
	if err != nil || got == nil {
		t.Fatalf("got %v, %v", got, err)
	}
	b, _ := json.MarshalIndent(got, "", " ")
	t.Log(string(b))
}

func TestDecodeCPDLCWilco(t *testing.T) {
	got, err := DecodeApps("BA", "/UPGCAYA.AT1..B-LRD628C409D00341F", Downlink)
	if err != nil || got == nil {
		t.Fatalf("got %v, %v", got, err)
	}
	b, _ := json.MarshalIndent(got, "", " ")
	t.Log(string(b))
}
