package parser

import (
	"testing"

	"github.com/mvd-analyzer/qwdemo/mvd"
)

func TestParseUserInfoString_AuthKey(t *testing.T) {
	player := &mvd.PlayerInfo{}
	parseUserInfoString(`\name\Neophyte\team\red\*auth\Neophyte\topcolor\4\bottomcolor\4`, player)

	if player.Auth != "Neophyte" {
		t.Errorf("Auth = %q, want %q", player.Auth, "Neophyte")
	}
	if player.Name != "Neophyte" {
		t.Errorf("Name = %q, want %q", player.Name, "Neophyte")
	}
	if player.Team != "red" {
		t.Errorf("Team = %q, want %q", player.Team, "red")
	}
}

func TestParseUserInfoString_NoAuth(t *testing.T) {
	player := &mvd.PlayerInfo{}
	parseUserInfoString(`\name\splif\team\blue\topcolor\13\bottomcolor\13`, player)

	if player.Auth != "" {
		t.Errorf("Auth = %q, want empty", player.Auth)
	}
	if player.Name != "splif" {
		t.Errorf("Name = %q, want %q", player.Name, "splif")
	}
}

func TestStripChatMarkup(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"empty", "", ""},
		{"leading CR", "\rhello", "hello"},
		{"color code", "&c5afix&cffftext", "ixtext"},
		{"reset code", "&rwhite", "white"},
		{"sound trigger K", "going for quad!K", "going for quad"},
		{"sound trigger H", "team take!H", "team take"},
		{"macro braces", "{name}: hi", "name: hi"},
		{"macro brackets", "[loc]", "loc"},
		{
			"real teamsay fixture",
			"\r{&c5afbix&cfff}: coming [{quad.low}]",
			"bix: coming quad.low",
		},
		{
			"team status fixture",
			"\r{&c39faki&cfff}{&c39f:&cfff} 0/100 sng:80 [{ra.low}]",
			"aki: 0/100 sng:80 ra.low",
		},
		{"only sound trigger doesn't strip lowercase", "abc!k", "abc!k"},
		{"idempotent on cleaned", "bix: coming quad.low", "bix: coming quad.low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripChatMarkup(tc.in)
			if got != tc.want {
				t.Errorf("StripChatMarkup(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Idempotence: applying again should be a no-op.
			if again := StripChatMarkup(got); again != got {
				t.Errorf("not idempotent: StripChatMarkup(%q) = %q (after first pass %q)", got, again, got)
			}
		})
	}
}

func TestParseUserInfoString_AuthWithSpecialChars(t *testing.T) {
	player := &mvd.PlayerInfo{}
	parseUserInfoString(`\name\TestUser\team\blue\*auth\test_login-123`, player)

	if player.Auth != "test_login-123" {
		t.Errorf("Auth = %q, want %q", player.Auth, "test_login-123")
	}
}
