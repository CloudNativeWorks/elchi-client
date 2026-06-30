package services

import (
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/protobuf/proto"
)

func TestNeedsReassert(t *testing.T) {
	tests := []struct {
		name        string
		hasDesired  bool
		liveExists  bool
		liveMatches bool
		want        bool
	}{
		{"no desired state never touches anything", false, false, false, false},
		{"no desired state, live present, ignored", false, true, true, false},
		{"desired present, live missing -> recreate", true, false, false, true},
		{"desired present, live drifted -> reassert", true, true, false, true},
		{"desired present, live matches -> do nothing", true, true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsReassert(tt.hasDesired, tt.liveExists, tt.liveMatches); got != tt.want {
				t.Errorf("needsReassert(%v,%v,%v) = %v, want %v",
					tt.hasDesired, tt.liveExists, tt.liveMatches, got, tt.want)
			}
		})
	}
}

func TestReadDesiredInMissing(t *testing.T) {
	dir := t.TempDir()
	data, ok, err := readDesiredIn(dir, rsyslogDesiredFile)
	if err != nil {
		t.Fatalf("unexpected error for missing state file: %v", err)
	}
	if ok {
		t.Errorf("expected exists=false for missing state file, got true")
	}
	if data != nil {
		t.Errorf("expected nil data for missing state file, got %v", data)
	}
}

func TestPersistDesiredRoundTrip(t *testing.T) {
	dir := t.TempDir()

	want := &client.RequestRsyslog{
		RsyslogConfig: &client.RsyslogConfig{
			RsyslogOutput: &client.RsyslogOutput{
				Target:   "10.0.0.1",
				Port:     514,
				Protocol: "tcp",
			},
		},
	}

	if err := persistDesiredIn(dir, rsyslogDesiredFile, want); err != nil {
		t.Fatalf("persistDesiredIn failed: %v", err)
	}

	data, ok, err := readDesiredIn(dir, rsyslogDesiredFile)
	if err != nil || !ok {
		t.Fatalf("readDesiredIn after persist: ok=%v err=%v", ok, err)
	}

	got := &client.RequestRsyslog{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("unmarshal persisted state: %v", err)
	}

	if !proto.Equal(want, got) {
		t.Errorf("round-trip mismatch:\n want %v\n  got %v", want, got)
	}
}

// A second persist must overwrite the first (last-applied wins), not append/merge.
func TestPersistDesiredOverwrites(t *testing.T) {
	dir := t.TempDir()

	first := &client.RequestRsyslog{RsyslogConfig: &client.RsyslogConfig{
		RsyslogOutput: &client.RsyslogOutput{Target: "1.1.1.1", Port: 514, Protocol: "udp"}}}
	second := &client.RequestRsyslog{RsyslogConfig: &client.RsyslogConfig{
		RsyslogOutput: &client.RsyslogOutput{Target: "2.2.2.2", Port: 601, Protocol: "tcp"}}}

	if err := persistDesiredIn(dir, rsyslogDesiredFile, first); err != nil {
		t.Fatalf("persist first: %v", err)
	}
	if err := persistDesiredIn(dir, rsyslogDesiredFile, second); err != nil {
		t.Fatalf("persist second: %v", err)
	}

	data, _, err := readDesiredIn(dir, rsyslogDesiredFile)
	if err != nil {
		t.Fatalf("read after overwrite: %v", err)
	}
	got := &client.RequestRsyslog{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(second, got) {
		t.Errorf("expected last-applied config to win, got %v", got)
	}
}
