package platform

import (
	"os"
	"testing"
)

// requireKernel ends a test whose kernel prerequisite is missing: a skip
// locally, a failure where LT_REQUIRE_KERNEL=1 says the environment has it
// (CI's check job, TESTING.md).
func requireKernel(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("LT_REQUIRE_KERNEL") == "1" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format+" (LT_REQUIRE_KERNEL=1 fails here instead)", args...)
}
