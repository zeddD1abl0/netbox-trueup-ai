//go:build windows

package audit

import "golang.org/x/sys/windows"

// tokenAccount returns the account the process runs as, from its token: its
// security identifier, which every token holds, and its name as
// DOMAIN\account, or "" where it can't be looked up. user.Current fails
// for an account whose profile directory it can't find, as for some service
// accounts, though the token still says who the process runs as.
func tokenAccount() (name, sid string, err error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid = user.User.Sid.String()
	if account, domain, _, err := user.User.Sid.LookupAccount(""); err == nil {
		name = account
		if domain != "" {
			name = domain + `\` + account
		}
	}
	return name, sid, nil
}
