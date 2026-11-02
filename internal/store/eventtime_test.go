package store

import (
	"path/filepath"
	"testing"
	"time"
)

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestEventTime(t *testing.T) {
	rx := utc(2026, 10, 30, 15, 20)
	pos := utc(2026, 10, 27, 21, 48)
	for _, c := range []struct {
		name    string
		rx      time.Time
		kind    string
		pos     *time.Time
		decoded string
		want    time.Time
		basis   string
	}{
		{"nothing of its own", rx, "cpdlc", nil, `{"fields":{}}`, rx, AtReceipt},
		{"not JSON, or empty", rx, "unknown", nil, `not json`, rx, AtReceipt},
		{"an operations event's full time", rx, "ops_report", nil, `{"fields":{"ops":{"at":"2026-10-27T05:00:00Z"}}}`, utc(2026, 10, 27, 5, 0), AtEvent},
		{"an operations event with a time that does not read", rx, "ops_report", nil, `{"fields":{"ops":{"at":"yesterday"}}}`, rx, AtReceipt},
		{"an A350 report's time", rx, "miam", nil, `{"fields":{"a350":{"Time":"2026-10-27T04:53:20Z"}}}`, utc(2026, 10, 27, 4, 53).Add(20 * time.Second), AtEvent},
		{"an RTR report's, with no A350 time field", rx, "miam", nil, `{"fields":{"rtr":{"Time":"2026-10-27T04:53:20Z"}}}`, utc(2026, 10, 27, 4, 53).Add(20 * time.Second), AtEvent},
		{"an A350 report with the zero time has none", rx, "miam", nil, `{"fields":{"a350":{"Time":"0001-01-01T00:00:00Z"}}}`, rx, AtReceipt},
		{"and the zero time is not passed over for the next field", rx, "miam", nil, `{"fields":{"a350":{"Time":"0001-01-01T00:00:00Z"},"rtr":{"Time":"2026-10-27T04:53:20Z"}}}`, rx, AtReceipt},
		{"a REP081's position time", rx, "miam", &pos, `{"fields":{"rep081":{"Dest":"EDDM"}}}`, pos, AtEvent},
		{"a MIAM that is not a REP081 keeps its arrival", rx, "miam", &pos, `{"fields":{}}`, rx, AtReceipt},
		{"an ACMF report's position time", rx, "acmf_report", &pos, `{"fields":{}}`, pos, AtEvent},
		{"a kind that does not give the date keeps its arrival", rx, "adsc", &pos, `{"fields":{}}`, rx, AtReceipt},
		{"a kind that gives the date but no position time", rx, "posrpt", nil, `{"fields":{}}`, rx, AtReceipt},
		{"a day and time earlier this month", rx, "ops_report", nil, `{"fields":{"ops":{"info":{"day":"27","time":"11:58Z"}}}}`, utc(2026, 10, 27, 11, 58), AtEvent},
		{"a day later than today is last month's", utc(2026, 11, 2, 1, 0), "ops_report", nil, `{"fields":{"ops":{"info":{"day":"30","time":"23:10Z"}}}}`, utc(2026, 10, 30, 23, 10), AtEvent},
		{"the 31st when this month has 30 days: last month's", utc(2026, 11, 2, 1, 0), "ops_report", nil, `{"fields":{"ops":{"info":{"day":"31","time":"22:00"}}}}`, utc(2026, 10, 31, 22, 0), AtEvent},
		{"in January, December of the year before", utc(2027, 1, 1, 0, 30), "ops_report", nil, `{"fields":{"ops":{"info":{"day":"31","time":"23:50"}}}}`, utc(2026, 12, 31, 23, 50), AtEvent},
		{"a few minutes ahead of the arrival still counts as this month", utc(2026, 10, 27, 11, 30), "ops_report", nil, `{"fields":{"ops":{"info":{"day":"27","time":"12:15"}}}}`, utc(2026, 10, 27, 12, 15), AtEvent},
		{"a day that is not a day", rx, "ops_report", nil, `{"fields":{"ops":{"info":{"day":"0","time":"11:58Z"}}}}`, rx, AtReceipt},
		{"a time that is not a time", rx, "ops_report", nil, `{"fields":{"ops":{"info":{"day":"27","time":"later"}}}}`, rx, AtReceipt},
		{"a pre-departure clearance's day and time", rx, "departure_clearance", nil, `{"fields":{"pdc":{"day":"27","time":"21:48"}}}`, utc(2026, 10, 27, 21, 48), AtEvent},
		{"a flight due to leave later", rx, "ops_report", nil, `{"fields":{"ops":{"sched":"2026-10-30T22:30:00Z"}}}`, utc(2026, 10, 30, 22, 30), AtScheduled},
		{"a flight that was due to leave before it arrived", rx, "ops_report", nil, `{"fields":{"ops":{"sched":"2026-10-30T10:00:00Z"}}}`, rx, AtReceipt},
		{"its own time wins over its schedule", rx, "ops_report", nil, `{"fields":{"ops":{"at":"2026-10-27T05:00:00Z","sched":"2026-10-31T10:00:00Z"}}}`, utc(2026, 10, 27, 5, 0), AtEvent},
	} {
		got, basis := EventTime(c.rx, c.kind, c.pos, c.decoded)
		if !got.Equal(c.want) || basis != c.basis {
			t.Errorf("%s: %v %s, want %v %s", c.name, got, basis, c.want, c.basis)
		}
	}
}

// A message is stored with the time it counts as, and decoding it again recomputes that.
func TestMessageIsStoredWithItsTime(t *testing.T) {
	s := open(t)
	rx := utc(2026, 10, 29, 2, 21)
	s.AddMessage(Message{ID: 1, Hex: "780AA4", T: rx, Label: "H1", Text: "a", Kind: "miam", Decoded: `{"fields":{"rtr":{"Time":"2026-10-27T04:53:20Z","Dest":"EDDM"}}}`})
	ms, _ := s.Messages("780AA4", utc(2026, 10, 29, 0, 0), utc(2026, 10, 30, 0, 0), 10)
	if len(ms) != 0 {
		t.Fatalf("it arrived on the 29th but counts as the 27th: %+v", ms)
	}
	ms, _ = s.Messages("780AA4", utc(2026, 10, 27, 0, 0), utc(2026, 10, 28, 0, 0), 10)
	if len(ms) != 1 || !ms[0].At.Equal(utc(2026, 10, 27, 4, 53).Add(20*time.Second)) || ms[0].AtBasis != AtEvent || !ms[0].T.Equal(rx) {
		t.Fatalf("%+v", ms)
	}
	// decoded again, now without a time of its own
	m := ms[0]
	m.Decoded = `{"fields":{}}`
	if err := s.UpdateDecoded(m); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.Messages("780AA4", utc(2026, 10, 29, 0, 0), utc(2026, 10, 30, 0, 0), 10)
	if len(ms) != 1 || !ms[0].At.Equal(rx) || ms[0].AtBasis != AtReceipt {
		t.Errorf("%+v", ms)
	}
}

// A database from before the time was kept gets it for the messages it holds, when it is opened.
func TestEventTimesAreFilledInForOldMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rx := utc(2026, 10, 29, 2, 21)
	s.AddMessage(Message{ID: 1, Hex: "780AA4", T: rx, Label: "H1", Text: "a", Kind: "miam", Decoded: `{"fields":{"rtr":{"Time":"2026-10-27T04:53:20Z"}}}`})
	s.AddMessage(Message{ID: 2, Hex: "780AA4", T: rx, Label: "H1", Text: "b", Kind: "cpdlc", Decoded: `{}`})
	// as the database was before: no time on any message
	if _, err := s.db.Exec(`UPDATE message SET at_ts = NULL, at_basis = ''`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ms, _ := s.Messages("780AA4", utc(2026, 10, 20, 0, 0), utc(2026, 11, 1, 0, 0), 10)
	if len(ms) != 2 || !ms[0].At.Equal(utc(2026, 10, 27, 4, 53).Add(20*time.Second)) || ms[0].AtBasis != AtEvent || !ms[1].At.Equal(rx) || ms[1].AtBasis != AtReceipt {
		t.Errorf("%+v", ms)
	}
}
