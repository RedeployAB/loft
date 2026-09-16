package release

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	ok := map[string]Semver{
		"0.1.7":        {0, 1, 7},
		"v0.1.7":       {0, 1, 7},
		"1.2.3-rc1":    {1, 2, 3},
		"1.2.3+abc":    {1, 2, 3},
		"10.0.0":       {10, 0, 0},
		"v0.0.0":       {0, 0, 0},
		"1.2.3-rc1+md": {1, 2, 3},
	}
	for in, want := range ok {
		got, parsed := Parse(in)
		if !parsed || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, true", in, got, parsed, want)
		}
	}
	for _, in := range []string{"dev", "", "1.2", "1.2.3.4", "1.x.3", "01.2.3", "-1.2.3", "v", "1.2.-3", " 1.2.3"} {
		if got, parsed := Parse(in); parsed {
			t.Errorf("Parse(%q) = %+v, true; want false", in, got)
		}
	}
}

func TestLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.6", "0.1.7", true},
		{"0.1.7", "0.1.7", false},
		{"0.1.8", "0.1.7", false},
		{"0.1.10", "0.1.9", false}, // numeric, not lexical
		{"0.9.9", "1.0.0", true},
		{"1.0.0", "0.9.9", false},
	}
	for _, c := range cases {
		a, _ := Parse(c.a)
		b, _ := Parse(c.b)
		if got := a.Less(b); got != c.want {
			t.Errorf("%s < %s = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if s := (Semver{0, 1, 7}).String(); s != "v0.1.7" {
		t.Errorf("String() = %q", s)
	}
}

func TestUserAgentRoundTrip(t *testing.T) {
	prev := Version
	t.Cleanup(func() { Version = prev })

	Version = "v0.1.7"
	got, ok := ParseUserAgent(UserAgent())
	if !ok || got != (Semver{0, 1, 7}) {
		t.Fatalf("ParseUserAgent(%q) = %+v, %v", UserAgent(), got, ok)
	}
	Version = "dev"
	if _, ok := ParseUserAgent(UserAgent()); ok {
		t.Fatalf("a dev build's agent %q parsed as a release", UserAgent())
	}
	for _, ua := range []string{"", "Mozilla/5.0", "loft-client/0.0.1", "loft-cli/", "loft-cli/latest"} {
		if _, ok := ParseUserAgent(ua); ok {
			t.Errorf("ParseUserAgent(%q) parsed", ua)
		}
	}
	if v, ok := ParseUserAgent("loft-cli/0.1.4"); !ok || v != (Semver{0, 1, 4}) {
		t.Errorf("bare version without platform suffix: %+v %v", v, ok)
	}
}

func TestPolicyCheck(t *testing.T) {
	cur := Semver{0, 1, 6}
	cases := map[string]struct {
		p    Policy
		want string // substring of the error, "" for nil
	}{
		"supported":       {Policy{Min: "v0.1.0"}, ""},
		"exactly min":     {Policy{Min: "v0.1.6"}, ""},
		"too old":         {Policy{Min: "v0.1.7"}, "too old for this platform (needs v0.1.7 or newer)"},
		"blocked":         {Policy{Min: "v0.1.0", Blocked: []string{"0.1.6"}}, "v0.1.6 is blocked on this platform"},
		"blocked other":   {Policy{Min: "v0.1.0", Blocked: []string{"v0.1.5", "junk"}}, ""},
		"blocked wins":    {Policy{Min: "v0.1.7", Blocked: []string{"v0.1.6"}}, "blocked"},
		"empty policy":    {Policy{}, ""}, // a platform that predates the field imposes nothing
		"unparseable min": {Policy{Min: "latest"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.p.Check(cur)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), UpdateHint) {
				t.Fatalf("got %v, want an error containing %q and the update hint", err, tc.want)
			}
		})
	}
}
