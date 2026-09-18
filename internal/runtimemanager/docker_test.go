package runtimemanager

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  [][]string
	exists map[string]bool
}

type imageRunner struct{ calls [][]string }

func (f *imageRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := strings.Join(args, " ")
	switch key {
	case "container inspect agent":
		return "{}", nil
	case "inspect --format {{.Image}} agent":
		return "sha256:old", nil
	case "image inspect --format {{.Id}} agent:local":
		return "sha256:new", nil
	default:
		return "", nil
	}
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 1 && args[1] == "inspect" {
		if f.exists[args[0]+":"+args[2]] {
			if args[0] == "container" {
				return "true", nil
			}
			return "{}", nil
		}
		return "", fmt.Errorf("missing")
	}
	return "", nil
}
func TestEnsureAgentUsesHardenedFixedMounts(t *testing.T) {
	f := &fakeRunner{exists: map[string]bool{}}
	cfg := ManagerConfig{AgentImage: "agent:test", EgressImage: "egress:test", EgressNetwork: "codexbot-egress", SharedHome: "/srv/codexbot/shared", ManagerContainer: "manager", RuntimeToken: "secret"}
	id := "123e4567-e89b-12d3-a456-426614174000"
	if err := ensureAgentContainer(context.Background(), f, cfg, id); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, c := range f.calls {
		joined += strings.Join(c, " ") + "\n"
	}
	for _, want := range []string{"--tmpfs /run/codexbot:rw,nosuid,nodev,noexec,size=1m,uid=1000,gid=1000,mode=0700", "CODEXBOT_COLLABORATION_URL=http://manager:8081", "--read-only", "no-new-privileges:true", "seccomp=/etc/codexbot/chromium-seccomp.json", "type=bind,src=/srv/codexbot/shared,dst=/home/agent", "type=volume,src=codexbot-agent-" + id + "-profile,dst=/var/lib/codexbot/profile", "network create --internal"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in calls:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "CODEX_HOME=") {
		t.Fatalf("Codex home must use shared /home/agent/.codex:\n%s", joined)
	}
}

func TestRemoveStaleContainerWhenTagMoves(t *testing.T) {
	runner := &imageRunner{}
	if err := removeStaleContainer(context.Background(), runner, "agent", "agent:local"); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, call := range runner.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	for _, want := range []string{"stop --time 15 agent", "rm agent"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in calls:\n%s", want, joined)
		}
	}
}
