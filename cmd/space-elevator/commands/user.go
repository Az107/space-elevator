package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/spf13/cobra"

	"github.com/albertoruiz/space-elevator/internal/config"
	"github.com/albertoruiz/space-elevator/internal/store"
)

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Manage the dashboard account (local access)",
}

var UserCmd = userCmd

var (
	userResetUsername string
)

func init() {
	userResetCmd.Flags().StringVar(&userResetUsername, "username", "admin", "account to reset")
	userCmd.AddCommand(userResetCmd)
}

// userResetCmd is the lockout recovery path: it runs directly against
// the local SQLite DB, so it trusts whoever can already touch the host
// and skips the old-password check the web flow requires.
var userResetCmd = &cobra.Command{
	Use:   "reset-password",
	Short: "Set a new dashboard password (run on the host, e.g. over SSH)",
	RunE:  runUserResetPassword,
}

func runUserResetPassword(cmd *cobra.Command, _ []string) error {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(cfg.StateDir, "space-elevator.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := cmd.Context()
	u, err := st.GetUserByUsername(ctx, userResetUsername)
	if err != nil {
		// Lockout helper: the username may have been changed in the
		// dashboard after setup. With a single account there's no
		// ambiguity, so target it instead of failing.
		users, lerr := st.ListUsers(ctx)
		if lerr == nil && len(users) == 1 {
			u = users[0]
			fmt.Fprintf(cmd.ErrOrStderr(), "note: no user %q found; using the only account %q\n", userResetUsername, u.Username)
		} else {
			return fmt.Errorf("no user %q: %w", userResetUsername, err)
		}
	}

	fmt.Printf("Resetting password for %q.\n", u.Username)
	pwd, err := promptTwice(cmd)
	if err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := st.UpdateUserPassword(ctx, u.ID, string(hash)); err != nil {
		return err
	}
	// The old credential is gone — invalidate every live session on
	// all devices, not just unknown ones.
	if err := st.DeleteSessionsForUser(ctx, u.ID); err != nil {
		return err
	}
	fmt.Println("OK: password updated; all dashboard sessions were signed out.")
	return nil
}

// promptTwice reads a new password twice without echo and enforces the
// same 8-character minimum as the web setup flow.
func promptTwice(cmd *cobra.Command) (string, error) {
	in := int(os.Stdin.Fd())
	for {
		fmt.Print("New password: ")
		a, err := term.ReadPassword(in)
		fmt.Println()
		if err != nil {
			return "", err
		}
		fmt.Print("Confirm password: ")
		b, err := term.ReadPassword(in)
		fmt.Println()
		if err != nil {
			return "", err
		}
		if len(a) < 8 {
			fmt.Fprintln(cmd.ErrOrStderr(), "password must be at least 8 characters")
			continue
		}
		if string(a) != string(b) {
			fmt.Fprintln(cmd.ErrOrStderr(), "passwords don't match, try again")
			continue
		}
		return string(a), nil
	}
}
