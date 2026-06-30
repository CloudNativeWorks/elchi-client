package filebeat

import (
	"bytes"
	"strings"
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/protobuf/proto"
)

func sampleFilebeatRequest() *client.RequestFilebeat {
	return &client.RequestFilebeat{
		Inputs: []*client.FilebeatInput{
			{Type: "log", Enabled: true, Id: "a", Paths: []string{"/var/log/elchi/x.log"}},
		},
		TimestampProcessor: &client.TimestampProcessor{
			Field:   "ts",
			Layouts: []string{"ISO8601"},
			Test:    []string{"2020-01-01T00:00:00Z"},
		},
		DropFieldsProcessor: &client.DropFieldsProcessor{Fields: []string{"agent", "host"}},
		FilebeatOutput: &client.FilebeatOutput{
			Output: &client.FilebeatOutput_Logstash{
				Logstash: &client.LogstashOutput{Hosts: []string{"1.2.3.4:5044"}, Loadbalance: true},
			},
		},
	}
}

// RenderConfig is the single source of truth used both to write filebeat.yml AND by
// the reconcile loop to detect drift. If it is not byte-stable across calls, the
// reconcile loop would see phantom drift every tick and restart filebeat forever
// (the config embeds maps, whose marshal order must be deterministic). This pins
// that contract.
func TestFilebeatRenderConfigDeterministic(t *testing.T) {
	req := sampleFilebeatRequest()

	first, err := RenderConfig(req)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := RenderConfig(req)
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("RenderConfig is not deterministic between calls:\n--- first ---\n%s\n--- again ---\n%s", first, again)
		}
	}
}

// The reconcile loop renders the config it read back from the persisted proto, so a
// proto round-trip must render to the exact same bytes as the original — otherwise
// reconcile would re-assert (and restart) on every tick.
func TestFilebeatRenderConfigStableAcrossProtoRoundTrip(t *testing.T) {
	req := sampleFilebeatRequest()
	want, err := RenderConfig(req)
	if err != nil {
		t.Fatalf("render original: %v", err)
	}

	blob, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored := &client.RequestFilebeat{}
	if err := proto.Unmarshal(blob, restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got, err := RenderConfig(restored)
	if err != nil {
		t.Fatalf("render restored: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("render differs after proto round-trip (reconcile would flap):\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestFilebeatRenderConfigContent(t *testing.T) {
	out, err := RenderConfig(sampleFilebeatRequest())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{"filebeat.inputs", "output.logstash", "1.2.3.4:5044", "drop_fields"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered filebeat.yml missing %q:\n%s", want, s)
		}
	}
}
