package valkeystore

import "testing"

func TestNormAddr(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"LocalHost:6379":          "localhost:6379",
		"10.0.0.1:6379":           "10.0.0.1:6379",
		"[2001:DB8::1]:6379":      "[2001:db8::1]:6379",
		"node-1.Example.com:7000": "node-1.example.com:7000",
		"no-port":                 "no-port",
	} {
		if got := normAddr(in); got != want {
			t.Errorf("normAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
