package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"

	"github.com/PascalKraupner/relvo/internal/database"
)

// DiscoverDocker is optional, read-only discovery scoped to dir's Compose project.
// Callers can ignore an unavailable Docker installation and use other profiles.
func DiscoverDocker(ctx context.Context, dir string, cfg database.Config) ([]database.Config, error) {
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-directory", dir, "ps", "--format", "json")
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Docker Compose discovery: %w", ctx.Err())
		}
		// Do not include command output: Compose diagnostics may contain env values.
		return nil, fmt.Errorf("Docker Compose discovery failed; check Docker availability and the Compose project: %w", err)
	}
	return parseDocker(output, cfg)
}

type composeContainer struct {
	Service    string `json:"Service"`
	Publishers []struct {
		URL           string `json:"URL"`
		TargetPort    int    `json:"TargetPort"`
		PublishedPort int    `json:"PublishedPort"`
		Protocol      string `json:"Protocol"`
	} `json:"Publishers"`
}

func parseDocker(data []byte, cfg database.Config) ([]database.Config, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var containers []composeContainer
	if data[0] == '[' {
		if err := json.Unmarshal(data, &containers); err != nil {
			return nil, errors.New("invalid Docker Compose JSON list")
		}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var container composeContainer
			if err := decoder.Decode(&container); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, errors.New("invalid Docker Compose JSON stream")
			}
			containers = append(containers, container)
		}
	}
	port := cfg.Port
	if port == "" {
		port = "3306"
	}
	target, err := strconv.Atoi(port)
	if err != nil || target < 1 || target > 65535 {
		return nil, errors.New("Docker discovery requires a valid connection port")
	}
	var candidates []database.Config
	seen := make(map[string]bool)
	for _, container := range containers {
		if cfg.Host == "" || container.Service != cfg.Host {
			continue
		}
		for _, publisher := range container.Publishers {
			if publisher.TargetPort != target || publisher.PublishedPort < 1 || publisher.PublishedPort > 65535 || (publisher.Protocol != "" && publisher.Protocol != "tcp") {
				continue
			}
			host := publisher.URL
			if host == "0.0.0.0" || host == "::" || host == "" {
				host = "127.0.0.1"
			}
			candidate := cfg
			candidate.Host = host
			candidate.Port = strconv.Itoa(publisher.PublishedPort)
			candidate.Socket = ""
			address := net.JoinHostPort(host, candidate.Port)
			if seen[address] {
				continue
			}
			seen[address] = true
			candidate.Name = cfg.Name + " (Docker " + container.Service + " " + address + ")"
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}
