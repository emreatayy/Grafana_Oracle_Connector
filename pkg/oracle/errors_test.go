package oracle

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatError_KnownORA(t *testing.T) {
	msg := FormatError(errors.New("ORA-01017: invalid username/password; logon denied"))
	if !strings.Contains(msg, "01017") || !strings.Contains(msg, "invalid username or password") {
		t.Errorf("msg = %q, want mapped ORA-01017 hint", msg)
	}
}

// TestFormatError_F10Codes covers the report §13 "Test" scenarios (bad
// password / URL / connection) and the query errors the F10 map adds.
func TestFormatError_F10Codes(t *testing.T) {
	cases := []struct {
		raw  string
		want string // a substring of the expected hint
	}{
		{"ORA-12154: could not resolve the connect identifier specified", "connect identifier"},
		{"ORA-12505: listener does not currently know of SID given", "requested service"},
		{"ORA-12560: TNS:protocol adapter error", "protocol adapter"},
		{"ORA-01033: ORACLE initialization or shutdown in progress", "shutdown in progress"},
		{"ORA-28009: connection to server failed", "Net configuration"},
		{"ORA-00911: invalid character", "invalid character"},
		{"ORA-01722: invalid number", "invalid number"},
		{"ORA-06550: line 1, column 7:", "PL/SQL error"},
		{"ORA-04063: package body has errors", "object has errors"},
	}
	for _, tc := range cases {
		got := FormatError(errors.New(tc.raw))
		if !strings.Contains(got, tc.want) {
			t.Errorf("FormatError(%q) = %q, want substring %q", tc.raw, got, tc.want)
		}
	}
}

func TestFormatError_UnknownORA(t *testing.T) {
	msg := FormatError(errors.New("foo\nORA-12345: some detail\nbar"))
	if !strings.Contains(msg, "ORA-12345") || !strings.Contains(msg, "some detail") {
		t.Errorf("msg = %q, want ORA code + first detail line", msg)
	}
}

func TestFormatError_Network(t *testing.T) {
	msg := FormatError(errors.New("dial tcp 1.2.3.4:1521: i/o timeout"))
	if !strings.Contains(msg, "network error") {
		t.Errorf("msg = %q, want network error prefix", msg)
	}
}

func TestFormatError_Plain(t *testing.T) {
	msg := FormatError(errors.New("first line\nsecond line"))
	if msg != "first line" {
		t.Errorf("msg = %q, want first line only", msg)
	}
}

func TestFormatError_Nil(t *testing.T) {
	if got := FormatError(nil); got != "" {
		t.Errorf("FormatError(nil) = %q, want empty", got)
	}
}

func TestRedact(t *testing.T) {
	msg := Redact("user=s3cret host s3cret host", "s3cret")
	if strings.Contains(msg, "s3cret") {
		t.Errorf("msg = %q, password still present", msg)
	}
	if !strings.Contains(msg, "[redacted]") {
		t.Errorf("msg = %q, want redaction marker", msg)
	}
}

func TestRedact_EscapedForm(t *testing.T) {
	// A DSN embeds the URL-escaped password; both raw and escaped forms are cut.
	msg := Redact("oracle://u:p%40ss@h/xe", "p@ss")
	if strings.Contains(msg, "p%40ss") || strings.Contains(msg, "p@ss") {
		t.Errorf("msg = %q, escaped password still present", msg)
	}
}

func TestRedact_EmptySecret(t *testing.T) {
	if got := Redact("unchanged message", ""); got != "unchanged message" {
		t.Errorf("Redact with empty secret = %q, want unchanged", got)
	}
}
