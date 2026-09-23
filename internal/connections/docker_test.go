package connections

import (
	"strings"
	"testing"

	"github.com/PascalKraupner/relvo/internal/database"
)

func TestParseDocker(t *testing.T) {
	matching := `{"Service":"mysql","Publishers":[{"URL":"0.0.0.0","TargetPort":3306,"PublishedPort":13306,"Protocol":"tcp"},{"URL":"::","TargetPort":3306,"PublishedPort":13306,"Protocol":"tcp"},{"URL":"192.0.2.1","TargetPort":3306,"PublishedPort":23306,"Protocol":"tcp"},{"URL":"0.0.0.0","TargetPort":33060,"PublishedPort":13360,"Protocol":"tcp"},{"URL":"0.0.0.0","TargetPort":3306,"PublishedPort":33306,"Protocol":"udp"}]}`
	other := `{"Service":"other","Publishers":[{"URL":"0.0.0.0","TargetPort":3306,"PublishedPort":9999,"Protocol":"tcp"}]}`
	for name, data := range map[string]string{"list": "[" + other + "," + matching + "]", "jsonlines": other + "\n" + matching + "\n"} {
		t.Run(name, func(t *testing.T) {
			cfg := database.Config{Name: "app", Host: "mysql", User: "root", Password: "secret", Database: "app", Socket: "/tmp/old.sock", TLS: "true"}
			candidates, err := parseDocker([]byte(data), cfg)
			if err != nil || len(candidates) != 2 {
				t.Fatalf("candidates count %d, error %v", len(candidates), err)
			}
			if candidates[0].Host != "127.0.0.1" || candidates[0].Port != "13306" || candidates[1].Host != "192.0.2.1" || candidates[1].Port != "23306" {
				t.Fatal("incorrect published endpoints")
			}
			for _, candidate := range candidates {
				if candidate.Password != cfg.Password || candidate.User != cfg.User || candidate.Database != cfg.Database || candidate.TLS != cfg.TLS || candidate.Socket != "" || !strings.Contains(candidate.Name, "Docker mysql") || strings.Contains(candidate.Name, cfg.Password) {
					t.Fatal("incorrect candidate configuration or name")
				}
			}
			if cfg.Host != "mysql" || cfg.Port != "" {
				t.Fatal("input configuration mutated")
			}
		})
	}
}

func TestParseDockerEmptyAndInvalid(t *testing.T) {
	for _, data := range []string{"", "\n", "[]", `{"Service":"mysql","Publishers":null}`, `{"Service":"mysql","Publishers":[{"TargetPort":3306,"PublishedPort":0}]}`} {
		got, err := parseDocker([]byte(data), database.Config{Host: "mysql"})
		if err != nil || len(got) != 0 {
			t.Fatalf("empty discovery: count %d, error %v", len(got), err)
		}
	}
	for _, data := range []string{"not JSON", "[", "{}\ninvalid"} {
		if _, err := parseDocker([]byte(data), database.Config{Host: "mysql"}); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
}
