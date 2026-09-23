package connections

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Add validates and appends a uniquely named profile under an interprocess lock.
// All concurrent additions must use Add, not a separate Load/Save sequence.
func Add(path string, p Profile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	// Never unlink this sibling: replacing its inode would split the lock domain.
	// NONBLOCK lets us reject a FIFO without blocking while opening it.
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("open connection profiles lock: %w", err)
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("inspect connection profiles lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errors.New("connection profiles lock must be a regular file owned by the current user with one link")
	}
	if err := lock.Chmod(0600); err != nil {
		return fmt.Errorf("secure connection profiles lock: %w", err)
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("lock connection profiles: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	profiles, err := Load(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("connection profile requires a name")
	}
	for _, existing := range profiles {
		if existing.Name == p.Name {
			return errors.New("connection profile name already exists")
		}
	}
	if _, err := Resolve(p); err != nil {
		return fmt.Errorf("invalid connection profile: %w", err)
	}
	return Save(path, append(profiles, p))
}
