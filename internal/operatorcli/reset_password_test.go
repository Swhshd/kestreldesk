package operatorcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

const passwordCanary = "synthetic-canary-password"

func TestResetPasswordCommandAcceptsPasswordFromStdinWithoutEcho(t *testing.T) {
	var out, errOut bytes.Buffer
	var gotPassword string
	deps := Dependencies{
		ResetPassword: func(_ context.Context, configPath, username, password string) error {
			if configPath != "synthetic-config.yaml" || username != "synthetic-operator" {
				t.Fatal("command did not pass the selected local account and config")
			}
			gotPassword = password
			return nil
		},
	}
	code := Run(context.Background(), []string{"reset-password", "--username", "synthetic-operator", "--config", "synthetic-config.yaml", "--password-stdin"}, strings.NewReader(passwordCanary+"\n"), &out, &errOut, deps)
	if code != 0 || gotPassword != passwordCanary {
		t.Fatal("stdin password was not applied successfully")
	}
	if strings.Contains(out.String()+errOut.String(), passwordCanary) {
		t.Fatal("password appeared in command output")
	}
}

func TestResetPasswordCommandRejectsPlaintextPasswordFlagWithoutEcho(t *testing.T) {
	var out, errOut bytes.Buffer
	called := false
	deps := Dependencies{
		ResetPassword: func(context.Context, string, string, string) error {
			called = true
			return nil
		},
	}
	code := Run(context.Background(), []string{"reset-password", "--username", "synthetic-operator", "--password", passwordCanary}, strings.NewReader(""), &out, &errOut, deps)
	if code == 0 || called {
		t.Fatal("plaintext password argv was accepted")
	}
	if strings.Contains(out.String()+errOut.String(), passwordCanary) {
		t.Fatal("rejected argv password appeared in command output")
	}
}

func TestResetPasswordCommandRequiresSafeInputForNonTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	called := false
	deps := Dependencies{
		ResetPassword: func(context.Context, string, string, string) error {
			called = true
			return nil
		},
	}
	code := Run(context.Background(), []string{"reset-password", "--username", "synthetic-operator"}, strings.NewReader(passwordCanary), &out, &errOut, deps)
	if code == 0 || called {
		t.Fatal("non-terminal input was read without an explicit stdin option")
	}
	if strings.Contains(out.String()+errOut.String(), passwordCanary) {
		t.Fatal("password appeared in command output")
	}
}

func TestResetPasswordCommandUsesHiddenConfirmedTerminalInput(t *testing.T) {
	var out, errOut bytes.Buffer
	var prompts []string
	var gotPassword string
	deps := Dependencies{
		IsTerminal: true,
		ReadHiddenPassword: func(prompt string, _ io.Writer) (string, error) {
			prompts = append(prompts, prompt)
			return passwordCanary, nil
		},
		ResetPassword: func(_ context.Context, _, _, password string) error {
			gotPassword = password
			return nil
		},
	}
	code := Run(context.Background(), []string{"reset-password", "--username", "synthetic-operator"}, strings.NewReader(""), &out, &errOut, deps)
	if code != 0 || gotPassword != passwordCanary || len(prompts) != 2 || prompts[0] == prompts[1] {
		t.Fatal("terminal password was not collected twice through the hidden-input provider")
	}
	if strings.Contains(out.String()+errOut.String(), passwordCanary) {
		t.Fatal("terminal password appeared in command output")
	}
}

func TestResetPasswordCommandDoesNotEchoFailureDetails(t *testing.T) {
	var out, errOut bytes.Buffer
	deps := Dependencies{
		ResetPassword: func(context.Context, string, string, string) error {
			return errors.New(passwordCanary)
		},
	}
	code := Run(context.Background(), []string{"reset-password", "--username", "synthetic-operator", "--password-stdin"}, strings.NewReader(passwordCanary), &out, &errOut, deps)
	if code == 0 {
		t.Fatal("failed reset reported success")
	}
	if strings.Contains(out.String()+errOut.String(), passwordCanary) {
		t.Fatal("reset error details exposed a secret canary")
	}
}
