package healthcheck

import "testing"

func TestThresholdChecker(t *testing.T) {
	c := ThresholdChecker{}

	cases := []struct {
		name      string
		sig       Signal
		xidThresh int
		eccThresh int
		wantBad   bool
	}{
		{
			name:      "healthy node under all thresholds",
			sig:       Signal{XidErrorCount: 0, ECCErrorCount: 0},
			xidThresh: 5, eccThresh: 5,
			wantBad: false,
		},
		{
			name:      "xid errors cross threshold",
			sig:       Signal{XidErrorCount: 6},
			xidThresh: 5, eccThresh: 5,
			wantBad: true,
		},
		{
			name:      "ecc errors cross threshold",
			sig:       Signal{ECCErrorCount: 10},
			xidThresh: 5, eccThresh: 5,
			wantBad: true,
		},
		{
			name:      "thermal throttle always unhealthy regardless of thresholds",
			sig:       Signal{ThermalThrottle: true},
			xidThresh: 1000, eccThresh: 1000,
			wantBad: true,
		},
		{
			name:      "zero threshold disables that check",
			sig:       Signal{XidErrorCount: 999},
			xidThresh: 0, eccThresh: 5,
			wantBad: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBad, reason := c.IsUnhealthy(tc.sig, tc.xidThresh, tc.eccThresh)
			if gotBad != tc.wantBad {
				t.Fatalf("IsUnhealthy() = %v (reason=%q), want %v", gotBad, reason, tc.wantBad)
			}
			if gotBad && reason == "" {
				t.Fatalf("IsUnhealthy() returned true but no reason")
			}
		})
	}
}
