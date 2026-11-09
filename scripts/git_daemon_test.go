package scripts_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise the actual daemon command expanded by Make, against synthetic repos
// only. No shell recipe is executed and the listener must remain on loopback.
func TestGitDaemonExportsOnlyExplicitRepositories(t *testing.T) {
	for _, tool := range []string{"git", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	makefile, err := filepath.Abs("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	repositories := []string{checkout, filepath.Join(root, "argonaut-sync-fixtures-repo"), filepath.Join(root, "argonaut-history-repo"), filepath.Join(root, "private-sibling"), filepath.Join(checkout, "private-nested")}
	for _, path := range repositories {
		if out, err := exec.Command("git", "init", "--quiet", "--bare", path).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	_ = reservation.Close()
	dryRun := exec.Command("make", "--no-print-directory", "-n", "-f", makefile, "argocd-git-daemon", fmt.Sprintf("GIT_DAEMON_PORT=%d", port))
	dryRun.Dir = checkout
	out, err := dryRun.CombinedOutput()
	if err != nil {
		t.Fatalf("make dry run: %v: %s", err, out)
	}
	expanded := strings.ReplaceAll(strings.ReplaceAll(string(out), "\r\n", "\n"), "\\\n", " ")
	var command string
	for _, line := range strings.Split(expanded, "\n") {
		if start := strings.Index(line, "git daemon --"); start >= 0 && (strings.HasPrefix(line, "setsid ") || strings.HasPrefix(line, "git ")) {
			command = strings.SplitN(line[start:], ">/tmp/", 2)[0]
			break
		}
	}
	if command == "" {
		t.Fatal("daemon recipe not found")
	}
	words := regexp.MustCompile(`(?:[^\s"]+|"[^"]*")+`).FindAllString(command, -1)
	for i := range words {
		words[i] = strings.ReplaceAll(words[i], `"`, "")
	}
	if !strings.Contains(command, `--listen="127.0.0.1"`) || !strings.Contains(command, "--strict-paths") {
		t.Fatal("recipe no longer restricts listener and exports")
	}
	logFile, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = checkout, logFile, logFile
	if err := cmd.Start(); err != nil {
		log, _ := os.ReadFile(logFile.Name())
		t.Fatalf("daemon start: %v: %s", err, log)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pidFile := filepath.Join(checkout, ".argocd-gitdaemon.pid")
	var pidBytes []byte
	for attempt := 0; attempt < 30; attempt++ {
		pidBytes, err = os.ReadFile(pidFile)
		if err == nil && len(pidBytes) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Kill() }()
	address := fmt.Sprintf("127.0.0.1:%d", port)
	ready := false
	for attempt := 0; attempt < 30; attempt++ {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("daemon did not listen on loopback")
	}
	for _, name := range []string{"checkout", "argonaut-sync-fixtures-repo", "argonaut-history-repo", "private-sibling", "checkout/private-nested"} {
		out, err := exec.Command("git", "ls-remote", "git://"+address+"/"+name).CombinedOutput()
		allowed := name == "checkout" || strings.HasPrefix(name, "argonaut-")
		if allowed && err != nil {
			t.Errorf("allowed repository %s rejected: %v: %s", name, err, out)
		}
		if !allowed && err == nil {
			t.Errorf("private repository %s was exported", name)
		}
	}
}
