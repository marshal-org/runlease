package main

import "testing"

func TestQuoteRemoteCommand(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"single", []string{"uname"}, `'uname'`},
		{"multiple", []string{"uname", "-a"}, `'uname' '-a'`},
		{"with-spaces", []string{"echo", "hello world"}, `'echo' 'hello world'`},
		{"with-single-quote", []string{"echo", "it's"}, `'echo' 'it'\''s'`},
		{"with-dollar", []string{"echo", "$HOME"}, `'echo' '$HOME'`},
		{"empty-arg", []string{"true", ""}, `'true' ''`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteRemoteCommand(tc.argv)
			if got != tc.want {
				t.Errorf("quoteRemoteCommand(%v) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}
