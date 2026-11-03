package netboxtest

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

// maxPage caps how much of a NetBox web page Import reads. The import page
// is about 300 KB.
const maxPage = 8 << 20

// Import creates objects through NetBox's own bulk import, as a person does
// in NetBox's web UI: it signs in as the superuser, and submits data, in
// YAML, to the import page at path, such as /dcim/device-types/import/. data
// can hold many documents, separated by ---, each of which NetBox imports,
// all of them or none. For device types, that is the import that NetBox's
// documentation and devicetype-library describe, which creates the
// components a file lists too (BulkImportView in
// netbox/views/generic/bulk_views.py).
//
// It returns NetBox's errors, from the page it shows again, when NetBox
// refuses the import.
func (nb *NetBox) Import(ctx context.Context, path, data string) error {
	if !strings.HasPrefix(path, "/") || !strings.HasSuffix(path, "/import/") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("import to %q: the path must be an import page such as /dcim/device-types/import/", path)
	}
	s, err := nb.signIn(ctx)
	if err != nil {
		return err
	}
	status, location, page, err := s.post(ctx, path, url.Values{"data": {data}, "format": {"yaml"}})
	if err != nil {
		return fmt.Errorf("import to %s: %w", path, err)
	}
	// NetBox redirects to the list of what it imported, and shows the page
	// again with the errors when it refuses (BulkImportView.post).
	switch {
	case status == http.StatusFound && strings.Contains(location, "modified_by_request="):
		return nil
	case status == http.StatusOK:
		if errs := pageErrors(page); len(errs) > 0 {
			return fmt.Errorf("import to %s: NetBox refused it: %s", path, strings.Join(errs, "; "))
		}
		return fmt.Errorf("import to %s: NetBox refused it, but its page gives no reason", path)
	}
	return fmt.Errorf("import to %s: NetBox answered %d, not 302 to what it imported", path, status)
}

// session is a signed-in session with NetBox's web UI.
type session struct {
	baseURL string
	client  *http.Client
}

// signIn signs in to NetBox's web UI as the superuser that compose.yaml
// creates, with Django's CSRF protection, and returns the session.
func (nb *NetBox) signIn(ctx context.Context) (*session, error) {
	username, password, err := Superuser()
	if err != nil {
		return nil, err
	}
	if _, err := localEndpoint(nb.URL, "/"); err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("sign in to NetBox: %w", err)
	}
	// The same loopback-only transport as the API's, with cookies, and
	// never following redirects.
	s := &session{
		baseURL: strings.TrimSuffix(nb.URL, "/"),
		client:  &http.Client{Timeout: client.Timeout, Transport: client.Transport, CheckRedirect: client.CheckRedirect, Jar: jar},
	}
	// The login page sets the CSRF cookie that the form must repeat.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/login/", nil)
	if err != nil {
		return nil, fmt.Errorf("sign in to NetBox: %w", err)
	}
	status, _, _, err := s.do(req)
	switch {
	case err != nil:
		return nil, fmt.Errorf("sign in to NetBox: %w", err)
	case status != http.StatusOK:
		return nil, fmt.Errorf("sign in to NetBox: the login page answered %d", status)
	}
	status, _, _, err = s.post(ctx, "/login/", url.Values{"username": {username}, "password": {password}, "next": {"/"}})
	switch {
	case err != nil:
		return nil, fmt.Errorf("sign in to NetBox: %w", err)
	case status != http.StatusFound:
		// The page doesn't repeat the password, but quote nothing of it.
		return nil, fmt.Errorf("sign in to NetBox as %s: NetBox answered %d, not 302", username, status)
	}
	return s, nil
}

// post submits a form to a page, with the session's CSRF token, and returns
// the status, where a redirect points, and the page.
func (s *session) post(ctx context.Context, path string, form url.Values) (status int, location, page string, err error) {
	u, err := url.Parse(s.baseURL + path)
	if err != nil {
		return 0, "", "", err
	}
	token := ""
	for _, c := range s.client.Jar.Cookies(u) {
		if c.Name == "csrftoken" {
			token = c.Value
		}
	}
	if token == "" {
		return 0, "", "", errors.New("NetBox set no CSRF cookie")
	}
	form.Set("csrfmiddlewaretoken", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(req)
}

// do sends req and returns the status, where a redirect points, and up to
// maxPage bytes of the page.
func (s *session) do(req *http.Request) (status int, location, page string, err error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage+1))
	switch {
	case err != nil:
		return 0, "", "", fmt.Errorf("read NetBox's answer: %w", err)
	case len(body) > maxPage:
		return 0, "", "", fmt.Errorf("NetBox's answer is over %d bytes", maxPage)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(body), nil
}

var (
	// NetBox shows a form's errors under the field, and a record's in a
	// message, a toast with a danger header (utilities/templates).
	fieldError = regexp.MustCompile(`(?s)<div class="form-text text-danger"[^>]*>(.*?)</div>`)
	toastError = regexp.MustCompile(`(?s)toast-header text-bg-danger.*?<div class="toast-body">(.*?)</div>`)
	tag        = regexp.MustCompile(`<[^>]*>`)
)

// pageErrors returns the errors a page of NetBox's web UI shows, as text.
func pageErrors(page string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{fieldError, toastError} {
		for _, m := range re.FindAllStringSubmatch(page, -1) {
			text := strings.Join(strings.Fields(html.UnescapeString(tag.ReplaceAllString(m[1], " "))), " ")
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}
