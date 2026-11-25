// recover-token replaces a lost owner/admin credential with protected stdin.
// The service MUST be stopped; SQLite serialization does not retire live sessions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("recover-token", flag.ContinueOnError)
	flags.SetOutput(output)
	db := flags.String("db", "", "existing absolute server database path")
	id := flags.Int64("user-id", 0, "existing account ID")
	username := flags.String("username", "", "exact existing account name")
	version := flags.Int64("expected-version", -1, "version shown by -inspect")
	stopped := flags.Bool("stopped-service", false, "confirm service is stopped and backup and private replacement are retained")
	inspect := flags.Bool("inspect", false, "show non-secret target identity and credential version without replacing its token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*stopped || !filepath.IsAbs(*db) || *id <= 0 || *username == "" {
		return errors.New("require -stopped-service, -db absolute path, -user-id and -username; no positional secrets")
	}
	info, err := os.Lstat(*db)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("database must be an existing regular file, not a symlink")
	}
	st, err := datastore.NewProviderFactory(*db)
	if err != nil {
		return errors.New("could not open protected recovery database")
	}
	defer st.Close()
	tx, err := st.Tx(context.Background())
	if err != nil {
		return errors.New("could not start recovery transaction")
	}
	defer func() { _ = tx.Rollback() }()
	user, err := tx.GetUserByID(*id)
	if err != nil || user == nil || user.Username != *username {
		return errors.New("recovery target identity mismatch")
	}
	owner, err := tx.IsBootstrapUser(user.ID)
	if err != nil || (!owner && user.Role != model.RoleAdmin) {
		return errors.New("local recovery requires an existing owner or administrator")
	}
	current, err := tx.PersonalTokenVersion(user.ID)
	if err != nil {
		return errors.New("could not read recovery version")
	}
	if *inspect {
		_, err = fmt.Fprintf(output, "id=%d username=%q role=%s channel_scope=%d version=%d\n", user.ID, user.Username, user.Role.String(), user.ChannelScope, current)
		return err
	}
	if *version < 0 || current != *version {
		return errors.New("credential version changed; inspect the target again before a new explicit attempt")
	}
	if file, ok := input.(*os.File); ok {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 || (info.Mode().IsRegular() && info.Mode().Perm()&0077 != 0) {
			return errors.New("replacement input must be protected redirected stdin, not a terminal or shared file")
		}
	}
	data, err := io.ReadAll(io.LimitReader(input, 67))
	if err != nil {
		return errors.New("could not read protected replacement")
	}
	candidate := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if !crypto.IsGeneratedToken(candidate) {
		return errors.New("replacement must be a freshly generated 64-character lowercase hexadecimal token")
	}
	hash := crypto.HashToken(candidate)
	if hash == user.PersonalTokenHash {
		return errors.New("replacement is already current; no credential changed")
	}
	changed, err := tx.ReplacePersonalToken(user.ID, current, user.PersonalTokenHash, hash, time.Now().UTC())
	if err != nil || !changed {
		return errors.New("credential replacement failed; retain the staged candidate and inspect before retrying")
	}
	// Exhaust pending setup retry material in this same transaction. The durable
	// owner marker, ID, role and scope remain untouched.
	if owner {
		finalized, err := tx.FinalizeBootstrapToken(user.ID)
		if err != nil || !finalized {
			return errors.New("could not finalize protected owner recovery")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("recovery commit outcome uncertain; retain candidate and inspect before retrying")
	}
	_, err = fmt.Fprintln(output, "Credential replaced. Owner marker, identity, role and scope preserved. Restart service and authenticate the retained candidate.")
	return err
}
