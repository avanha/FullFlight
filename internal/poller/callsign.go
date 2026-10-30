package poller

import "github.com/avanha/fullflight/internal/airlines"

// CallsignCandidates returns the callsigns to try for a flight number: see airlines.CallsignCandidates.
func CallsignCandidates(flight string) ([]string, error) { return airlines.CallsignCandidates(flight) }
