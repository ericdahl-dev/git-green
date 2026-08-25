package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCommand_versionAndHelp(t *testing.T) {
	cases := []struct {
		args        []string
		wantHandled bool
		wantSubstr  string
	}{
		{args: nil, wantHandled: false},
		{args: []string{}, wantHandled: false},
		{args: []string{"--badflag"}, wantHandled: false},
		{args: []string{"help"}, wantHandled: true, wantSubstr: "Usage:"},
		{args: []string{"-help"}, wantHandled: true, wantSubstr: "Usage:"},
		{args: []string{"--help"}, wantHandled: true, wantSubstr: "Usage:"},
		{args: []string{"version"}, wantHandled: true, wantSubstr: "git-green dev"},
		{args: []string{"-version"}, wantHandled: true, wantSubstr: "git-green dev"},
		{args: []string{"--version"}, wantHandled: true, wantSubstr: "git-green dev"},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var buf bytes.Buffer
			code, handled := runCommand(tc.args, &buf)
			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tc.wantHandled)
			}
			if !handled {
				if buf.Len() != 0 {
					t.Errorf("unhandled args wrote %q, want nothing", buf.String())
				}
				return
			}
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if !strings.Contains(buf.String(), tc.wantSubstr) {
				t.Errorf("output = %q, want it to contain %q", buf.String(), tc.wantSubstr)
			}
		})
	}
}

// The Homebrew formula's test block runs "git-green --help"; it has to exit 0
// without a config file present.
func TestRunCommand_helpNeedsNoConfig(t *testing.T) {
	var buf bytes.Buffer
	code, handled := runCommand([]string{"--help"}, &buf)
	if !handled || code != 0 {
		t.Fatalf("--help: handled=%v code=%d, want true/0", handled, code)
	}
	if !strings.Contains(buf.String(), "git-green") {
		t.Errorf("help output does not name the binary: %q", buf.String())
	}
}
