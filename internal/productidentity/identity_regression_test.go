package productidentity

import "testing"

func TestSignedBundleProcessNamesRemainManageable(t *testing.T) {
	for _, name := range []string{"canary", "ibkr", "canary-darwin-arm64", "canary-darwin-amd64", "canary-linux-arm64", "canary-linux-amd64"} {
		if !IsManagedProcessExecutableBase(name) {
			t.Fatalf("reject %s", name)
		}
	}
	for _, name := range []string{"canary-helper", "canary-linux-arm64.sh", "canary-linux-386", "/tmp/canary", "sh", "fake-canary"} {
		if IsManagedProcessExecutableBase(name) {
			t.Fatalf("accept %s", name)
		}
	}
}
