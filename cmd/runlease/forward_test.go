package main

import "testing"

func TestParseForwardSpec(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		dir     forwardDirection
		want    forwardSpec
		wantErr bool
	}{
		{
			name: "port:host:port",
			raw:  "6443:localhost:6443",
			dir:  dirLocal,
			want: forwardSpec{dir: dirLocal, listenPort: 6443, targetHost: "localhost", targetPort: 6443},
		},
		{
			name: "bind:port:host:port",
			raw:  "0.0.0.0:8080:web:80",
			dir:  dirLocal,
			want: forwardSpec{dir: dirLocal, bind: "0.0.0.0", listenPort: 8080, targetHost: "web", targetPort: 80},
		},
		{
			name: "port:port shorthand",
			raw:  "9090:9091",
			dir:  dirRemote,
			want: forwardSpec{dir: dirRemote, listenPort: 9090, targetHost: "localhost", targetPort: 9091},
		},
		{name: "garbage", raw: "nope", dir: dirLocal, wantErr: true},
		{name: "non-numeric port", raw: "abc:host:6443", dir: dirLocal, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseForwardSpec(tc.raw, tc.dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseForwardSpec(%q, %v) = %+v, want %+v", tc.raw, tc.dir, got, tc.want)
			}
		})
	}
}

func TestForwardSpec_listenAddr_defaultsToLoopback(t *testing.T) {
	s := forwardSpec{dir: dirLocal, listenPort: 1234}
	if got := s.listenAddr(); got != "127.0.0.1:1234" {
		t.Errorf("listenAddr = %q, want 127.0.0.1:1234", got)
	}
}

func TestForwardSpec_listenAddr_explicitBind(t *testing.T) {
	s := forwardSpec{dir: dirLocal, bind: "0.0.0.0", listenPort: 80}
	if got := s.listenAddr(); got != "0.0.0.0:80" {
		t.Errorf("listenAddr = %q, want 0.0.0.0:80", got)
	}
}
