package provision

import (
	"os"
	"testing"
)

func TestProvisionPermissionMask(t *testing.T) {
	for _, test := range []struct {
		mode    os.FileMode
		broader bool
	}{
		{0o600, false},
		{0o400, false},
		{0o000, false},
		{0o700, true},
		{0o640, true},
		{0o604, true},
	} {
		if got := permissionsBroaderThan(test.mode, 0o600); got != test.broader {
			t.Fatalf("mode %04o broader = %v, want %v", test.mode, got, test.broader)
		}
	}
}
