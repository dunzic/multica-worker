package service

import "testing"

func TestRoleSourceOutboxRetryDelayIsBounded(t *testing.T) {
	tests := []struct {
		attempt int16
		want    string
	}{
		{1, "5s"}, {2, "10s"}, {5, "1m20s"}, {9, "21m20s"}, {20, "21m20s"},
	}
	for _, test := range tests {
		if got := roleSourceOutboxRetryDelay(test.attempt).String(); got != test.want {
			t.Errorf("attempt %d delay=%s, want %s", test.attempt, got, test.want)
		}
	}
}
