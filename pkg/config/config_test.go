package config

import "testing"

func TestNormalizeRelayAddr(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"wss://connect.runlease.online":    "wss://connect.runlease.online",
		"https://connect.runlease.online":  "wss://connect.runlease.online",
		"HTTPS://connect.runlease.online/": "wss://connect.runlease.online",
		"http://127.0.0.1:8080":            "ws://127.0.0.1:8080",
		"ws://127.0.0.1:8080/":             "ws://127.0.0.1:8080",
		" relay.example.com:7777 ":         "relay.example.com:7777",
		"wss://relay.example.com/custom":   "wss://relay.example.com/custom",
	} {
		if got := NormalizeRelayAddr(in); got != want {
			t.Errorf("NormalizeRelayAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
