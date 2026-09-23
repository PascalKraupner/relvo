package connections

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PascalKraupner/relvo/internal/database"
)

func addProfile(name string) Profile {
	return Profile{Name: name, Config: database.Config{Host: "localhost", User: "root", Database: "app", Password: "not-persisted"}}
}

func TestAddConcurrent(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nested", "connections.json")
			const count = 24
			start := make(chan struct{})
			results := make(chan error, count)
			for i := range count {
				go func() {
					<-start
					name := fmt.Sprintf("profile-%d", i)
					if duplicate {
						name = "same"
					}
					results <- Add(path, addProfile(name))
				}()
			}
			close(start)
			successes := 0
			for range count {
				select {
				case err := <-results:
					if err == nil {
						successes++
					} else if !duplicate || !strings.Contains(err.Error(), "already exists") {
						t.Fatalf("Add: %v", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("concurrent Add timed out")
				}
			}
			want := count
			if duplicate {
				want = 1
			}
			profiles, err := Load(path)
			if err != nil || len(profiles) != want || successes != want {
				t.Fatalf("profiles=%d successes=%d want=%d error=%v", len(profiles), successes, want, err)
			}
			seen := make(map[string]bool)
			for _, p := range profiles {
				if seen[p.Name] || p.Config.Password != "" {
					t.Fatal("duplicate profile or persisted password")
				}
				seen[p.Name] = true
			}
		})
	}
}

func TestAddProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const count = 8
	results := make(chan error, count)
	for i := range count {
		go func() {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAddProcessHelper$")
			cmd.Env = append(os.Environ(), "RELVO_ADD_TEST_PATH="+path, fmt.Sprintf("RELVO_ADD_TEST_NAME=process-%d", i))
			output, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("child Add: %w: %s", err, output)
			}
			results <- err
		}()
	}
	for range count {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	profiles, err := Load(path)
	if err != nil || len(profiles) != count {
		t.Fatalf("concurrent processes saved %d profiles, want %d: %v", len(profiles), count, err)
	}
}

func TestAddProcessHelper(t *testing.T) {
	path := os.Getenv("RELVO_ADD_TEST_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	if err := Add(path, addProfile(os.Getenv("RELVO_ADD_TEST_NAME"))); err != nil {
		t.Fatal(err)
	}
}

func TestAddLockPermissionsAndStability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	writeEnv(t, path+".lock", "")
	if err := os.Chmod(path+".lock", 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := Add(path, addProfile(name)); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(path + ".lock")
	if err != nil || after.Mode().Perm() != 0600 || !os.SameFile(before, after) {
		t.Fatal("lock must remain private with a stable inode")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("profiles must remain private")
	}
}

func TestAddRejectsUnsafeLocks(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "fifo", "hardlink", "wrong-owner"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "connections.json")
			lock := path + ".lock"
			var err error
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(dir, "target")
				writeEnv(t, target, "untouched")
				if kind == "symlink" {
					err = os.Symlink(target, lock)
				} else {
					err = os.Link(target, lock)
				}
			case "directory":
				err = os.Mkdir(lock, 0700)
			case "fifo":
				err = syscall.Mkfifo(lock, 0600)
			case "wrong-owner":
				if os.Geteuid() != 0 {
					t.Skip("changing ownership requires root")
				}
				writeEnv(t, lock, "")
				err = os.Chown(lock, 1, -1)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Add(path, addProfile("unsafe")); err == nil {
				t.Fatal("unsafe lock accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("unsafe lock must not create profiles")
			}
		})
	}
}

func TestAddValidationDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := Add(path, addProfile("existing")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Profile{addProfile("existing"), addProfile("  "), {Name: "invalid"}} {
		if err := Add(path, p); err == nil {
			t.Fatal("invalid or duplicate profile accepted")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected addition changed saved profiles")
	}
	writeEnv(t, path, "invalid JSON")
	if err := Add(path, addProfile("new")); err == nil {
		t.Fatal("invalid existing JSON must fail")
	}
	after, err = os.ReadFile(path)
	if err != nil || string(after) != "invalid JSON" {
		t.Fatal("invalid existing JSON was overwritten")
	}
}
