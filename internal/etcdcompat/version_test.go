package etcdcompat

import "testing"

func TestServer(t *testing.T) {
	for _, version := range []string{"3.6.0", "3.6.8", "3.6.99", "3.7.0", "3.7.3", "3.7.123"} {
		if !Server(version) {
			t.Errorf("supported server rejected: %q", version)
		}
	}
	for _, version := range []string{"", "garbage", "3.5.99", "3.8.0", "4.6.0", "2.7.0", "3.6", "v3.6.8", "3.6.8-rc.1", "3.7.0-alpha.0", "3.7.3+build", "3.6.08", "3.7.-1", "3.6.8\n", " 3.7.3"} {
		if Server(version) {
			t.Errorf("unsupported server accepted: %q", version)
		}
	}
}

func TestStorage(t *testing.T) {
	for _, version := range []string{"3.6.0", "3.7.0"} {
		if !Storage(version) {
			t.Errorf("supported storage rejected: %q", version)
		}
	}
	for _, version := range []string{"", "garbage", "3.5.0", "3.8.0", "4.6.0", "3.6.8", "3.7.3", "3.6.0-rc.1", "3.7.0+build", "3.6.00", "3.7.0\n"} {
		if Storage(version) {
			t.Errorf("unsupported storage accepted: %q", version)
		}
	}
}
