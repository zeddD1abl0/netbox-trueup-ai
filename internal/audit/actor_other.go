//go:build !windows

package audit

import "errors"

// tokenAccount is Windows's: elsewhere the user is found by its user ID.
func tokenAccount() (name, sid string, err error) {
	return "", "", errors.New("only Windows reads the account from the process's token")
}
