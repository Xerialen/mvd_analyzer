package main

import "testing"

// TestDispatch pins the subcommand resolution (FIX 3): flags and empty args
// default to serve; known positionals route to their command; an unknown
// positional (a typo'd subcommand) is rejected loudly instead of silently
// booting a server.
func TestDispatch(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantCmd string
		wantOK  bool
	}{
		{"empty", nil, "serve", true},
		{"flags only", []string{"-addr", ":9000"}, "serve", true},
		{"explicit serve", []string{"serve", "-addr", ":9000"}, "serve", true},
		{"version", []string{"version"}, "version", true},
		{"cache", []string{"cache", "stats"}, "cache", true},
		{"keys", []string{"keys", "list"}, "keys", true},
		{"typo subcommand", []string{"serv"}, "serv", false},
		{"unknown positional", []string{"bogus", "-x"}, "bogus", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, ok := dispatch(c.args)
			if cmd != c.wantCmd || ok != c.wantOK {
				t.Errorf("dispatch(%v) = (%q, %v); want (%q, %v)", c.args, cmd, ok, c.wantCmd, c.wantOK)
			}
		})
	}
}
