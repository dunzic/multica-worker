package drlock

import "testing"

func TestAdvisoryLockKeyIsStableAndDistinct(t *testing.T) {
	const migrationLockKey int64 = 7244554146635925501
	if AdvisoryLockKey == 0 || AdvisoryLockKey == migrationLockKey {
		t.Fatalf("invalid DR lock key: %d", AdvisoryLockKey)
	}
}
