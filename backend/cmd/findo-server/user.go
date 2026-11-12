package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/httpapi"
	"github.com/thoff/findo/backend/internal/index"
)

const userUsage = `usage:
  findo-server user add [--admin] <name>   create an account and print its enrollment link
  findo-server user enroll <name>          print a fresh link (a new device, or recovery after losing every passkey)

Run it on the machine that runs Findo, as the user that runs it. The link lasts
15 minutes and works once.`

// userCommand runs "findo-server user ...". Running it is the proof of
// authority: anyone with access to the server can already read its database,
// so it needs no sign-in of its own. This is how the first admin is made, and
// how an account is recovered.
func userCommand(args []string, idx *index.Index, masterKey []byte, publicURL *url.URL, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(userUsage)
	}
	switch args[0] {
	case "add":
		return userAdd(args[1:], idx, masterKey, publicURL, out)
	case "enroll":
		return userEnroll(args[1:], idx, publicURL, out)
	}
	return fmt.Errorf("unknown user command %q\n%s", args[0], userUsage)
}

func userAdd(args []string, idx *index.Index, masterKey []byte, publicURL *url.URL, out io.Writer) error {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	admin := fs.Bool("admin", false, "make the user an administrator")
	// Allow the flag before or after the name.
	var name string
	for rest := args; len(rest) > 0; {
		if err := fs.Parse(rest); err != nil {
			return fmt.Errorf("%v\n%s", err, userUsage)
		}
		rest = fs.Args()
		if len(rest) > 0 {
			if name != "" {
				return fmt.Errorf("expected one name\n%s", userUsage)
			}
			name, rest = rest[0], rest[1:]
		}
	}
	if name == "" {
		return fmt.Errorf("a name is required\n%s", userUsage)
	}
	if !index.ValidUsername(name) {
		return errors.New("a name is 1 to 64 letters, digits, dots, underscores or hyphens")
	}

	count, err := idx.CountUsers()
	if err != nil {
		return err
	}
	if count == 0 && !*admin {
		return errors.New("the first account must be an administrator: findo-server user add --admin " + name)
	}
	role := index.RoleUser
	if *admin {
		role = index.RoleAdmin
	}
	user, err := idx.CreateUser(name, role)
	if err != nil {
		return err
	}

	if count == 0 {
		// Volumes added before accounts existed belong to the first admin.
		adopted, err := httpapi.AdoptUnownedVolumes(idx, masterKey, user.ID)
		if err != nil {
			_ = idx.DeleteUser(user.ID) // leave things as they were so this can be retried
			return fmt.Errorf("could not hand the existing volumes to the new admin: %w", err)
		}
		if adopted > 0 {
			fmt.Fprintf(out, "%s now owns the %d volume(s) that were already configured.\n", user.Username, adopted)
		}
	}
	fmt.Fprintf(out, "Created %s %q.\n", role, user.Username)
	return printLink(idx, publicURL, user, out)
}

func userEnroll(args []string, idx *index.Index, publicURL *url.URL, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("a name is required\n%s", userUsage)
	}
	user, ok, err := idx.GetUserByName(args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no user named %q", args[0])
	}
	if user.Disabled {
		return fmt.Errorf("%q is disabled; an administrator can enable the account first", user.Username)
	}
	return printLink(idx, publicURL, user, out)
}

func printLink(idx *index.Index, publicURL *url.URL, user index.User, out io.Writer) error {
	creds, err := idx.ListCredentials(user.ID)
	if err != nil {
		return err
	}
	purpose := index.EnrollFirst
	if len(creds) > 0 {
		purpose = index.EnrollRecovery
	}
	link, expires, err := auth.IssueEnrollment(idx, publicURL, user.ID, purpose, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nOpen this link on the device to register a passkey (once, before %s):\n\n  %s\n\n", expires.Format(time.Kitchen), link)
	if publicURL.Hostname() == "localhost" {
		fmt.Fprintln(out, "Note: FINDO_PUBLIC_URL isn't set, so passkeys are bound to localhost and only work in a browser on this machine.")
	}
	return nil
}
