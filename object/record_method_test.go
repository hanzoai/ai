package object

import "testing"

// TestRecordedGatesOnMethod pins the rule the record filter asks before it builds
// anything: under logPostOnly a GET is not kept, so composing a record for one is
// work nothing reads.
func TestRecordedGatesOnMethod(t *testing.T) {
	prior := logPostOnly
	defer func() { logPostOnly = prior }()

	logPostOnly = true
	if Recorded("GET") {
		t.Error("GET is recorded under logPostOnly; it is discarded in prepareRecord")
	}
	for _, m := range []string{"POST", "PATCH", "DELETE"} {
		if !Recorded(m) {
			t.Errorf("%s is not recorded under logPostOnly; only GET is dropped", m)
		}
	}

	logPostOnly = false
	for _, m := range []string{"GET", "POST"} {
		if !Recorded(m) {
			t.Errorf("%s is not recorded with logPostOnly off", m)
		}
	}
}

// TestPrepareRecordAgreesWithRecorded holds the filter's early return and the
// store's own check to one answer, so a request cannot be skipped by one and kept
// by the other.
func TestPrepareRecordAgreesWithRecorded(t *testing.T) {
	prior := logPostOnly
	defer func() { logPostOnly = prior }()
	logPostOnly = true

	for _, m := range []string{"GET", "POST"} {
		ok, err := prepareRecord(&Record{Method: m, Action: "models"}, nil, nil)
		if err != nil {
			t.Fatalf("prepareRecord(%s): %v", m, err)
		}
		if ok != Recorded(m) {
			t.Errorf("%s: prepareRecord kept=%v but Recorded=%v", m, ok, Recorded(m))
		}
	}
}
