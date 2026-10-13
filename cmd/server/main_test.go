// This file has been modified for the KestrelDesk project.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerBootstrapFailureExitsNonzero(t *testing.T) {
	const childFlag = "KESTRELDESK_SERVER_EXIT_TEST_CHILD"
	const configPathEnv = "KESTRELDESK_SERVER_EXIT_TEST_CONFIG"
	if os.Getenv(childFlag) == "1" {
		os.Args = []string{os.Args[0], "-config", os.Getenv(configPathEnv)}
		main()
		return
	}

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	dsn := filepath.Join(tempDir, "server.db")
	configText := fmt.Sprintf("db:\n  type: sqlite\n  dsn: %q\nvectorDB:\n  type: \"\"\n", dsn)
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatalf("write isolated test config: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestServerBootstrapFailureExitsNonzero$")
	cmd.Env = append(os.Environ(),
		childFlag+"=1",
		configPathEnv+"="+configPath,
		"KESTRELDESK_ENV_FILE="+os.DevNull,
		"ENV_FILE="+os.DevNull,
		"KESTRELDESK_AUTH_BOOTSTRAPADMINPASSWORD=",
		"BOOTSTRAP_ADMIN_PASSWORD=",
		"KESTRELDESK_DB_DSN=",
		"DB_DSN=",
		"DATABASE_URL=",
		"KESTRELDESK_DB_TYPE=",
		"DB_TYPE=",
		"DATABASE_TYPE=",
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("server child did not exit with status 1 after bootstrap failure; output category present=%t", strings.Contains(output.String(), "bootstrap admin credential configuration is required"))
	}
	if !strings.Contains(output.String(), "bootstrap admin credential configuration is required") {
		t.Fatal("server child output did not identify the missing bootstrap configuration")
	}
}
