package sysinfo

import (
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name            string
		in              string
		wantName, wantV string
	}{
		{
			name: "ubuntu quoted",
			in: `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
ID=ubuntu`,
			wantName: "Ubuntu", wantV: "24.04",
		},
		{
			name:     "unquoted fedora",
			in:       "NAME=Fedora Linux\nVERSION_ID=40\n",
			wantName: "Fedora Linux", wantV: "40",
		},
		{
			name:     "single quotes and CRLF",
			in:       "NAME='Alpine Linux'\r\nVERSION_ID='3.20.1'\r\n",
			wantName: "Alpine Linux", wantV: "3.20.1",
		},
		{
			name:     "rolling release without VERSION_ID falls back to VERSION",
			in:       "NAME=\"Arch Linux\"\nVERSION=\"rolling\"\n",
			wantName: "Arch Linux", wantV: "rolling",
		},
		{
			name:     "comments and blank lines are ignored",
			in:       "# NAME=Fake\n\n#VERSION_ID=9\nNAME=Real\n",
			wantName: "Real", wantV: "",
		},
		{
			name:     "value containing equals sign",
			in:       "NAME=\"a=b\"\n",
			wantName: "a=b", wantV: "",
		},
		{name: "empty input"},
		{name: "garbage without equals", in: "this is not an os-release file\n\x00\xff"},
		{name: "lone equals and empty keys", in: "=\n=x\n  = y\n"},
		{name: "unterminated quote", in: "NAME=\"Oops\nVERSION_ID='\n", wantName: "\"Oops", wantV: "'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, version := parseOSRelease(tc.in)
			if name != tc.wantName || version != tc.wantV {
				t.Errorf("parseOSRelease = (%q, %q), want (%q, %q)", name, version, tc.wantName, tc.wantV)
			}
		})
	}
}

func TestParseMeminfo(t *testing.T) {
	const sample = `MemTotal:       16299644 kB
MemFree:         1234567 kB
MemAvailable:    9876543 kB
Buffers:          123456 kB
`
	tests := []struct {
		name         string
		in           string
		total, avail uint64
	}{
		{name: "typical", in: sample, total: 16299644 * 1024, avail: 9876543 * 1024},
		{name: "no MemAvailable (old kernel)", in: "MemTotal: 2048 kB\nMemFree: 512 kB\n", total: 2048 * 1024},
		{name: "wrong unit ignored", in: "MemTotal: 4 MB\nMemAvailable: 8 kB\n", avail: 8 * 1024},
		{name: "non-numeric value", in: "MemTotal: lots kB\nMemAvailable: 1 kB\n", avail: 1024},
		{name: "negative value", in: "MemTotal: -5 kB\n"},
		{name: "overflowing value", in: "MemTotal: 18446744073709551615 kB\n"},
		{name: "missing colon", in: "MemTotal 100 kB\n"},
		{name: "empty", in: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			total, avail := parseMeminfo(tc.in)
			if total != tc.total || avail != tc.avail {
				t.Errorf("parseMeminfo = (%d, %d), want (%d, %d)", total, avail, tc.total, tc.avail)
			}
		})
	}
}

func TestParseLoadavg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want [3]float64
	}{
		{name: "typical", in: "0.52 0.41 0.38 1/345 12345\n", want: [3]float64{0.52, 0.41, 0.38}},
		{name: "high load", in: "12.50 8.00 3.25 9/100 1\n", want: [3]float64{12.5, 8, 3.25}},
		{name: "too few fields", in: "0.52 0.41\n"},
		{name: "empty", in: ""},
		{name: "non-numeric field", in: "x 0.41 0.38 1/2 3\n", want: [3]float64{0, 0.41, 0.38}},
		{name: "NaN and Inf rejected", in: "NaN Inf -1\n"},
		{name: "huge value kept finite", in: "1e300 0 0\n", want: [3]float64{1e300, 0, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLoadavg(tc.in); got != tc.want {
				t.Errorf("parseLoadavg = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseUptime(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{name: "typical", in: "302345.67 1200000.00\n", want: 302345},
		{name: "zero", in: "0.00 0.00\n", want: 0},
		{name: "empty", in: "", want: 0},
		{name: "negative", in: "-10 0\n", want: 0},
		{name: "NaN", in: "NaN 0\n", want: 0},
		{name: "out of range", in: "1e30 0\n", want: 0},
		{name: "not a number", in: "soon\n", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseUptime(tc.in); got != tc.want {
				t.Errorf("parseUptime = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParseCPUModel(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			name: "x86 first core wins",
			in: "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Xeon(R) CPU @ 2.20GHz\n" +
				"processor\t: 1\nmodel name\t: Other\n",
			want: "Intel(R) Xeon(R) CPU @ 2.20GHz",
		},
		{name: "arm without model name", in: "processor : 0\nCPU implementer : 0x41\n"},
		{name: "empty", in: ""},
		{name: "key without value", in: "model name\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCPUModel(tc.in); got != tc.want {
				t.Errorf("parseCPUModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// The parsers must tolerate arbitrary input without panicking.
func TestParsersDoNotPanic(t *testing.T) {
	inputs := []string{"", "\x00", strings.Repeat("=", 1000), "NAME=", "MemTotal:", "a:b:c kB", "\n\n\n"}
	for _, in := range inputs {
		parseOSRelease(in)
		parseMeminfo(in)
		parseLoadavg(in)
		parseUptime(in)
		parseCPUModel(in)
	}
}
