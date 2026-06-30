package common

import (
	"errors"
	"testing"
)

func TestTempSiblingPath(t *testing.T) {
	cases := map[string]string{
		"/etc/rsyslog.d/50-elchi.conf": "/etc/rsyslog.d/.50-elchi.conf.elchi-tmp",
		"/etc/filebeat/filebeat.yml":   "/etc/filebeat/.filebeat.yml.elchi-tmp",
		"/var/lib/elchi/x":             "/var/lib/elchi/.x.elchi-tmp",
	}
	for dst, want := range cases {
		if got := TempSiblingPath(dst); got != want {
			t.Errorf("TempSiblingPath(%q) = %q, want %q", dst, got, want)
		}
	}
}

// The temp must not match a wildcard *.conf include, or rsyslog would load it
// before it is committed.
func TestTempSiblingPathNotDotConf(t *testing.T) {
	got := TempSiblingPath("/etc/rsyslog.d/50-elchi.conf")
	if got[len(got)-5:] == ".conf" {
		t.Errorf("temp path %q ends in .conf and would be picked up by a wildcard include", got)
	}
}

func TestClassifyValidatorResult(t *testing.T) {
	tests := []struct {
		name    string
		exitErr error
		output  string
		want    ValidationOutcome
	}{
		{
			name:    "clean exit is valid",
			exitErr: nil,
			output:  "Config OK",
			want:    ConfigValid,
		},
		{
			name:    "filebeat genuine yaml error is invalid",
			exitErr: errors.New("exit status 1"),
			output:  "Exiting: error loading config file: yaml: line 4: did not find expected ',' or ']'",
			want:    ConfigInvalid,
		},
		{
			name:    "filebeat missing keystore cannot run",
			exitErr: errors.New("exit status 1"),
			output:  "Exiting: could not initialize the keystore: open /var/lib/filebeat/filebeat.keystore: permission denied",
			want:    ConfigValidatorUnavailable,
		},
		{
			name:    "rsyslog permission denied opening file cannot run",
			exitErr: errors.New("exit status 1"),
			output:  "rsyslogd: could not open config file '/etc/rsyslog.d/.50-elchi.conf.elchi-tmp': Permission denied",
			want:    ConfigValidatorUnavailable,
		},
		{
			name:    "rsyslog parse error is invalid",
			exitErr: errors.New("exit status 1"),
			output:  "rsyslogd: error during parsing file, on or before line 2: invalid character",
			want:    ConfigInvalid,
		},
		{
			name:    "missing binary cannot run",
			exitErr: errors.New("exec: \"filebeat\": executable file not found in $PATH"),
			output:  "",
			want:    ConfigValidatorUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyValidatorResult(tt.exitErr, tt.output); got != tt.want {
				t.Errorf("ClassifyValidatorResult(%v, %q) = %v, want %v", tt.exitErr, tt.output, got, tt.want)
			}
		})
	}
}
