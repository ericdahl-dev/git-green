package aggregator

import "testing"

func TestStatusMappings(t *testing.T) {
	cases := []struct {
		status   RunStatus
		expected Stoplight
	}{
		{StatusSuccess, StoplightGreen},
		{StatusNeutral, StoplightGreen},
		{StatusSkipped, StoplightGreen},
		{StatusFailure, StoplightRed},
		{StatusTimedOut, StoplightRed},
		{StatusActionRequired, StoplightRed},
		{StatusQueued, StoplightYellow},
		{StatusInProgress, StoplightYellow},
		{StatusCancelled, StoplightGrey},
	}
	for _, tc := range cases {
		got := Aggregate([]RunStatus{tc.status})
		if got != tc.expected {
			t.Errorf("status %q: expected %v, got %v", tc.status, tc.expected, got)
		}
	}
}

func TestEmptyReturnsGrey(t *testing.T) {
	if got := Aggregate(nil); got != StoplightGrey {
		t.Errorf("expected grey for empty, got %v", got)
	}
}

func TestAllCancelledReturnsGrey(t *testing.T) {
	got := Aggregate([]RunStatus{StatusCancelled, StatusCancelled})
	if got != StoplightGrey {
		t.Errorf("expected grey, got %v", got)
	}
}

func TestMixedGreenYellowReturnsYellow(t *testing.T) {
	got := Aggregate([]RunStatus{StatusSuccess, StatusInProgress})
	if got != StoplightYellow {
		t.Errorf("expected yellow, got %v", got)
	}
}

func TestAnyRedOverridesAll(t *testing.T) {
	got := Aggregate([]RunStatus{StatusSuccess, StatusInProgress, StatusFailure})
	if got != StoplightRed {
		t.Errorf("expected red, got %v", got)
	}
}

func TestRedBeatsYellow(t *testing.T) {
	got := Aggregate([]RunStatus{StatusQueued, StatusTimedOut})
	if got != StoplightRed {
		t.Errorf("expected red, got %v", got)
	}
}

func TestGreenOnlyReturnsGreen(t *testing.T) {
	got := Aggregate([]RunStatus{StatusSuccess, StatusNeutral, StatusSkipped})
	if got != StoplightGreen {
		t.Errorf("expected green, got %v", got)
	}
}
