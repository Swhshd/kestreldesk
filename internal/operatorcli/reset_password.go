package operatorcli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/Swhshd/kestreldesk/internal/bootstrap"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/term"
)

const maximumPasswordInputBytes = 4096

type Dependencies struct {
	IsTerminal         bool
	ReadHiddenPassword func(prompt string, output io.Writer) (string, error)
	ResetPassword      func(ctx context.Context, configPath, username, password string) error
}

func SystemDependencies() Dependencies {
	return Dependencies{
		IsTerminal: term.IsTerminal(int(os.Stdin.Fd())),
		ReadHiddenPassword: func(prompt string, output io.Writer) (string, error) {
			if _, err := io.WriteString(output, prompt); err != nil {
				return "", err
			}
			password, err := term.ReadPassword(int(os.Stdin.Fd()))
			_, _ = io.WriteString(output, "\n")
			return string(password), err
		},
		ResetPassword: resetPassword,
	}
}

// Run handles the local `admin reset-password` command. It emits only static
// diagnostics and never prints supplied passwords or database errors.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, deps Dependencies) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, "Expected: admin reset-password --username NAME [--config PATH] [--password-stdin]\n")
		return 2
	}
	if args[0] != "reset-password" {
		_, _ = io.WriteString(stderr, "Unknown admin command.\n")
		return 2
	}
	args = args[1:]
	for _, arg := range args {
		if arg == "--password" || arg == "-password" || strings.HasPrefix(arg, "--password=") || strings.HasPrefix(arg, "-password=") {
			_, _ = io.WriteString(stderr, "Plaintext password arguments are not supported. Use hidden terminal input or --password-stdin.\n")
			return 2
		}
	}

	flags := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	username := flags.String("username", "", "employee account username")
	configPath := flags.String("config", "config/config.yaml", "path to config file")
	passwordStdin := flags.Bool("password-stdin", false, "read the password from stdin")
	help := flags.Bool("help", false, "show command help")
	if err := flags.Parse(args); err != nil {
		_, _ = io.WriteString(stderr, "Invalid command options.\n")
		return 2
	}
	if *help {
		_, _ = io.WriteString(stdout, "Usage: admin reset-password --username NAME [--config PATH] [--password-stdin]\n")
		_, _ = io.WriteString(stdout, "Without --password-stdin, password input is hidden and confirmed in a terminal.\n")
		return 0
	}
	if flags.NArg() != 0 || strings.TrimSpace(*username) == "" {
		_, _ = io.WriteString(stderr, "A username is required and extra arguments are not accepted.\n")
		return 2
	}

	var password string
	if *passwordStdin {
		data, err := io.ReadAll(io.LimitReader(stdin, maximumPasswordInputBytes+1))
		if err != nil || len(data) > maximumPasswordInputBytes {
			_, _ = io.WriteString(stderr, "Password input could not be read.\n")
			return 2
		}
		password = stripSingleLineEnding(string(data))
		if strings.ContainsAny(password, "\r\n") {
			_, _ = io.WriteString(stderr, "Password input must contain exactly one line.\n")
			return 2
		}
	} else {
		if !deps.IsTerminal || deps.ReadHiddenPassword == nil {
			_, _ = io.WriteString(stderr, "Use --password-stdin when running without a terminal.\n")
			return 2
		}
		first, err := deps.ReadHiddenPassword("New password: ", stdout)
		if err != nil {
			_, _ = io.WriteString(stderr, "Hidden password input failed.\n")
			return 2
		}
		confirmation, err := deps.ReadHiddenPassword("Confirm new password: ", stdout)
		if err != nil {
			_, _ = io.WriteString(stderr, "Hidden password input failed.\n")
			return 2
		}
		if first != confirmation {
			_, _ = io.WriteString(stderr, "Password confirmation did not match.\n")
			return 2
		}
		password = first
	}
	if password == "" {
		_, _ = io.WriteString(stderr, "A non-empty password is required.\n")
		return 2
	}
	if deps.ResetPassword == nil {
		_, _ = io.WriteString(stderr, "Password reset is unavailable.\n")
		return 1
	}
	if err := deps.ResetPassword(ctx, *configPath, strings.TrimSpace(*username), password); err != nil {
		_, _ = io.WriteString(stderr, "Password reset failed. Check the account and local database configuration.\n")
		return 1
	}
	_, _ = io.WriteString(stdout, "Password reset completed; active account sessions were revoked.\n")
	return 0
}

func stripSingleLineEnding(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
}

func resetPassword(ctx context.Context, configPath, username, password string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return errors.New("operator database initialization failed")
	}
	db, err := bootstrap.InitDBQuiet(cfg.DB)
	if err != nil {
		return errors.New("operator database initialization failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("operator database initialization failed")
	}
	previousDB := sqls.DB()
	defer func() {
		sqls.SetDB(previousDB)
		_ = sqlDB.Close()
	}()
	service := services.NewLocalOperatorPasswordResetService(nil)
	if err := service.ResetPassword(ctx, username, password); err != nil {
		return errors.New("local operator password reset failed")
	}
	return nil
}
