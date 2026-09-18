package runtimemanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type commandRunner interface {
	Run(context.Context, ...string) (string, error)
}
type dockerCLI struct{ binary string }

func (d dockerCLI) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.binary, args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		operation := "command"
		if len(args) > 0 {
			operation = args[0]
		}
		return "", fmt.Errorf("docker %s failed: %w: %s", operation, err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
func exists(ctx context.Context, r commandRunner, kind, name string) bool {
	_, err := r.Run(ctx, kind, "inspect", name)
	return err == nil
}

func containerUsesImage(ctx context.Context, r commandRunner, containerName, imageName string) (bool, error) {
	containerImage, err := r.Run(ctx, "inspect", "--format", "{{.Image}}", containerName)
	if err != nil {
		return false, err
	}
	desiredImage, err := r.Run(ctx, "image", "inspect", "--format", "{{.Id}}", imageName)
	if err != nil {
		return false, err
	}
	return containerImage == desiredImage, nil
}

func removeStaleContainer(ctx context.Context, r commandRunner, name, image string) error {
	if !exists(ctx, r, "container", name) {
		return nil
	}
	matches, err := containerUsesImage(ctx, r, name, image)
	if err != nil {
		return err
	}
	if matches {
		return nil
	}
	_, _ = r.Run(ctx, "stop", "--time", "15", name)
	if _, err = r.Run(ctx, "rm", name); err != nil {
		return fmt.Errorf("replace stale container %s: %w", name, err)
	}
	return nil
}

type ManagerConfig struct {
	AgentImage       string
	EgressImage      string
	EgressNetwork    string
	SharedHome       string
	ManagerContainer string
	RuntimeToken     string
	ControlPlaneURL  string
}

func ensureAgentContainer(ctx context.Context, r commandRunner, cfg ManagerConfig, id string) error {
	name := "codexbot-agent-" + id
	network := name
	egressNetwork := cfg.EgressNetwork
	if egressNetwork == "" {
		egressNetwork = "codexbot-egress"
	}
	profile := name + "-profile"
	egress := name + "-egress"
	if !exists(ctx, r, "network", network) {
		if _, err := r.Run(ctx, "network", "create", "--internal", "--label", "io.codexbot.managed=true", network); err != nil {
			return err
		}
	}
	if !exists(ctx, r, "volume", profile) {
		if _, err := r.Run(ctx, "volume", "create", "--label", "io.codexbot.managed=true", profile); err != nil {
			return err
		}
	}
	if cfg.ManagerContainer != "" {
		_, _ = r.Run(ctx, "network", "connect", network, cfg.ManagerContainer)
	}
	if cfg.EgressImage != "" && !exists(ctx, r, "network", egressNetwork) {
		if _, err := r.Run(ctx, "network", "create", "--label", "io.codexbot.managed=true", egressNetwork); err != nil {
			return err
		}
	}
	if cfg.EgressImage != "" {
		if err := removeStaleContainer(ctx, r, egress, cfg.EgressImage); err != nil {
			return err
		}
	}
	if cfg.EgressImage != "" && !exists(ctx, r, "container", egress) {
		if _, err := r.Run(ctx, "create", "--name", egress, "--restart", "unless-stopped", "--label", "io.codexbot.managed=true", "--network", network, "--network-alias", "egress", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "128", "--memory", "256m", cfg.EgressImage); err != nil {
			return err
		}
		if _, err := r.Run(ctx, "start", egress); err != nil {
			return err
		}
	}
	if cfg.EgressImage != "" {
		if state, err := r.Run(ctx, "inspect", "--format", "{{.State.Running}}", egress); err == nil && state != "true" {
			if _, err = r.Run(ctx, "start", egress); err != nil {
				return err
			}
		} else if err == nil {
			health, _ := r.Run(ctx, "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{end}}", egress)
			if health == "unhealthy" {
				if _, err = r.Run(ctx, "restart", "--time", "10", egress); err != nil {
					return err
				}
			}
		}
		// Reconcile network membership on every start. Docker reports an error
		// when the endpoint already exists, which is safe to ignore here.
		_, _ = r.Run(ctx, "network", "connect", egressNetwork, egress)
	}
	if err := removeStaleContainer(ctx, r, name, cfg.AgentImage); err != nil {
		return err
	}
	if exists(ctx, r, "container", name) {
		state, err := r.Run(ctx, "inspect", "--format", "{{.State.Running}}", name)
		if err == nil && state == "true" {
			health, _ := r.Run(ctx, "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{end}}", name)
			if health == "unhealthy" {
				_, err = r.Run(ctx, "restart", "--time", "15", name)
				return err
			}
			return nil
		}
		_, err = r.Run(ctx, "start", name)
		return err
	}
	workerToken := derivedSecret(cfg.RuntimeToken, "worker:"+id)
	kasmPassword := derivedSecret(cfg.RuntimeToken, "kasm:"+id)[:24]
	// The image runs as UID 1000 with no effective initial capabilities and
	// no-new-privileges. Do not cap-drop ALL here: Chromium needs sys_chroot in
	// its nested user namespace for the sandbox. The pinned seccomp profile
	// also permits Bubblewrap's mount operations inside a nested user namespace;
	// it does not grant SYS_ADMIN or disable the outer seccomp filter.
	args := []string{"create", "--name", name, "--hostname", name, "--restart", "unless-stopped", "--label", "io.codexbot.managed=true", "--label", "io.codexbot.agent=" + id, "--network", network, "--read-only", "--security-opt", "no-new-privileges:true", "--security-opt", "seccomp=/etc/codexbot/chromium-seccomp.json", "--pids-limit", "512", "--memory", "4g", "--cpus", "2", "--shm-size", "1g", "--tmpfs", "/tmp:rw,nosuid,nodev,size=1g", "--tmpfs", "/run:rw,nosuid,nodev,size=256m", "--tmpfs", "/run/codexbot:rw,nosuid,nodev,noexec,size=1m,uid=1000,gid=1000,mode=0700", "--mount", "type=bind,src=" + cfg.SharedHome + ",dst=/home/agent", "--mount", "type=volume,src=" + profile + ",dst=/var/lib/codexbot/profile", "-e", "CODEXBOT_AGENT_ID=" + id, "-e", "CODEXBOT_WORKER_TOKEN=" + workerToken, "-e", "CODEXBOT_PROFILE_DIR=/var/lib/codexbot/profile", "-e", "XDG_CONFIG_HOME=/var/lib/codexbot/profile/xdg/config", "-e", "XDG_DATA_HOME=/var/lib/codexbot/profile/xdg/data", "-e", "XDG_CACHE_HOME=/var/lib/codexbot/profile/xdg/cache", "-e", "XDG_STATE_HOME=/var/lib/codexbot/profile/xdg/state", "-e", "CHROMIUM_USER_DATA_DIR=/var/lib/codexbot/profile/chromium", "-e", "KASM_USER=codexbot", "-e", "KASM_PASSWORD=" + kasmPassword, "-e", "HTTP_PROXY=http://egress:3128", "-e", "HTTPS_PROXY=http://egress:3128", "-e", "ALL_PROXY=http://egress:3128", "-e", "NO_PROXY=127.0.0.1,localhost", cfg.AgentImage}
	if cfg.ManagerContainer != "" {
		args = append(args[:len(args)-1], "-e", "CODEXBOT_COLLABORATION_URL=http://"+cfg.ManagerContainer+":8081", cfg.AgentImage)
	}
	if _, err := r.Run(ctx, args...); err != nil {
		return err
	}
	_, err := r.Run(ctx, "start", name)
	return err
}
func stopAgentContainer(ctx context.Context, r commandRunner, id string) error {
	name := "codexbot-agent-" + id
	if !exists(ctx, r, "container", name) {
		return nil
	}
	_, err := r.Run(ctx, "stop", "--time", "15", name)
	return err
}
func inspectIP(ctx context.Context, r commandRunner, id string) (string, error) {
	name := "codexbot-agent-" + id
	network := name
	out, err := r.Run(ctx, "inspect", "--format", "{{json .NetworkSettings.Networks}}", name)
	if err != nil {
		return "", err
	}
	var networks map[string]struct{ IPAddress string }
	if err = json.Unmarshal([]byte(out), &networks); err != nil {
		return "", err
	}
	if n, ok := networks[network]; ok && n.IPAddress != "" {
		return n.IPAddress, nil
	}
	return "", errors.New("agent network address unavailable")
}
