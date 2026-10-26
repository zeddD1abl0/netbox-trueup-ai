package filelock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// holdEnv, when set, makes the test binary hold a lock on the file it names,
// as another process would, until stdin closes or it is killed. holdSharedEnv
// makes the lock shared.
const (
	holdEnv       = "FILELOCK_TEST_HOLD"
	holdSharedEnv = "FILELOCK_TEST_HOLD_SHARED"
)

func TestMain(m *testing.M) {
	if path := os.Getenv(holdEnv); path != "" {
		os.Exit(hold(path, os.Getenv(holdSharedEnv) != ""))
	}
	os.Exit(m.Run())
}

func hold(path string, shared bool) int {
	l, err := take(context.Background(), path, shared, time.Minute, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = l.Unlock()
	return 0
}

func TestConflicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		first, second bool // shared
		conflict      bool
	}{
		{"exclusive, then exclusive", false, false, true},
		{"exclusive, then shared", false, true, true},
		{"shared, then exclusive", true, false, true},
		{"shared, then shared", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "lock")
			first, err := take(t.Context(), path, tt.first, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			second, err := take(t.Context(), path, tt.second, 100*time.Millisecond, nil)
			switch {
			case tt.conflict && !errors.Is(err, ErrTimeout):
				t.Errorf("the second lock = %v, want an error wrapping ErrTimeout", err)
			case !tt.conflict && err != nil:
				t.Errorf("the second lock: %v", err)
			case err == nil:
				if err := second.Unlock(); err != nil {
					t.Error(err)
				}
			}
			if err := first.Unlock(); err != nil {
				t.Fatal(err)
			}
			// Once the first lock goes, the second can be taken.
			again, err := take(t.Context(), path, tt.second, 0, nil)
			if err != nil {
				t.Fatalf("the second lock once the first is released: %v", err)
			}
			if err := again.Unlock(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestWaiting(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")
	held, err := Exclusive(t.Context(), path, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A lock released while another waits goes to the waiter, which says
	// it waited.
	go func() {
		time.Sleep(1200 * time.Millisecond)
		_ = held.Unlock()
	}()
	waited := false
	l, err := Shared(t.Context(), path, 10*time.Second, func() { waited = true })
	if err != nil {
		t.Fatalf("Shared while another lock is released: %v", err)
	}
	if !waited {
		t.Error("onWait wasn't called after a second's wait")
	}

	// A context that ends stops the wait with its error.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := Exclusive(ctx, path, time.Minute, nil); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		t.Errorf("Exclusive with a context that ends = %v, want the context's error", err)
	}
	if err := l.Unlock(); err != nil {
		t.Error(err)
	}
}

func TestFileMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't use Unix file modes")
	}
	path := filepath.Join(t.TempDir(), "lock")
	l, err := Shared(t.Context(), path, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Unlock() }()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the lock file: %v, %v; want mode 0600", info, err)
	}
}

func TestReleasedWhenHolderDies(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%v", shared), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "lock")
			helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // G204: the test binary itself
			helper.Env = append(os.Environ(), holdEnv+"="+path)
			if shared {
				helper.Env = append(helper.Env, holdSharedEnv+"=1")
			}
			stdin, err := helper.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close() }()
			stdout, err := helper.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			helper.Stderr = os.Stderr
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
				t.Fatalf("the helper said %q, %v", line, err)
			}
			if _, err := Exclusive(t.Context(), path, 200*time.Millisecond, nil); !errors.Is(err, ErrTimeout) {
				t.Fatalf("Exclusive while another process holds the lock = %v, want it to wait and give up", err)
			}

			// Killed, the helper can't release the lock itself.
			if err := helper.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = helper.Wait()
			l, err := Exclusive(t.Context(), path, 5*time.Second, nil)
			if err != nil {
				t.Fatalf("Exclusive after the holder died: %v", err)
			}
			if err := l.Unlock(); err != nil {
				t.Error(err)
			}
		})
	}
}
