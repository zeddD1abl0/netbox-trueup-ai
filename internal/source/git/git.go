// Package git is the Git source kind. It fetches the latest commit of a
// branch or tag with go-git, so that TrueUp needs no git executable, and
// reads that commit's tree from Git's object store without checking it out
// (docs/adr/0020-git-sources-with-go-git.md).
//
// Each fetch makes a shallow, single-branch, bare clone, with a partial-clone
// filter that leaves out files larger than the source's max_file_size. The
// clone goes into the source's cache, in a directory named for its commit,
// and replaces the previous one once it is complete, so a failed fetch
// leaves the previous clone in place. A fetch first asks the repository
// which commit its branch or tag is at, and downloads nothing when the cache
// already holds it.
//
// Repositories are untrusted (boundary B1 in docs/architecture.md):
//
//   - only https is used, with TLS verification always on, no redirects, and
//     a proxy only from HTTPS_PROXY; every other scheme is refused before
//     go-git sees it, and nothing is read from Git's own configuration,
//     credential helpers or ~/.ssh;
//   - a fetch is limited in time (fetch_timeout), in what it writes to disk
//     (max_fetch_size), and in how many objects it may hold;
//   - text from the server reaches errors cut short, and URLs in logs and
//     errors carry no user name or password.
package git

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-billy/v6/osfs"
	gogit "github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

// maxObjects is the most objects a fetch may bring. A shallow clone holds a
// commit, perhaps a tag, and a tree or blob for each entry of the commit's
// tree, of which the source layer walks at most a million.
const maxObjects = 1100000

// Options configure the Git kind.
type Options struct {
	// CacheDir is the root of the source caches, normally
	// <data_dir>/cache/sources (source.CacheDir).
	CacheDir string
	// RootCAs, if set, are the certificates trusted for a server's
	// certificate, instead of the system's.
	RootCAs *x509.CertPool
	// Logger gets a record for each fetch. Nil means no logging.
	Logger *slog.Logger
}

// Kind fetches Git sources. It is safe for concurrent use, though fetches of
// one source at once download it twice.
type Kind struct {
	cacheDir   string
	http       *http.Client
	log        *slog.Logger
	maxObjects uint32
	now        func() time.Time
}

var _ source.Kind = (*Kind)(nil)

// New returns the Git kind.
func New(opts Options) *Kind {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Kind{
		cacheDir:   opts.CacheDir,
		http:       newHTTPClient(opts.RootCAs, http.ProxyFromEnvironment),
		log:        logger,
		maxObjects: maxObjects,
		now:        time.Now,
	}
}

// Fetch brings the source's cache up to date with the latest commit of its
// branch or tag, and returns that commit's content.
func (k *Kind) Fetch(ctx context.Context, spec source.Spec) (source.Content, error) {
	if err := checkURL(spec.URL); err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, spec.Limits.FetchTimeout)
	defer cancel()
	c, err := k.fetch(fetchCtx, spec)
	if err != nil && ctx.Err() == nil && errors.Is(fetchCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("the fetch took longer than fetch_timeout, %s: %w", spec.Limits.FetchTimeout, err)
	}
	return c, err
}

// Open returns the content the last successful Fetch left in the cache,
// without the network.
func (k *Kind) Open(ctx context.Context, spec source.Spec) (source.Content, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := source.CacheDir(k.cacheDir, spec.Name)
	l, err := readLatest(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, source.ErrNotFetched
	case err != nil:
		return nil, fmt.Errorf("%w: its cache can't be read: %w", source.ErrNotFetched, err)
	case l.URL != spec.URL || l.Requested != spec.Ref:
		return nil, fmt.Errorf("%w since its url or ref changed", source.ErrNotFetched)
	case !l.covers(spec.Limits.MaxFileSize):
		return nil, fmt.Errorf("%w since its max_file_size was raised", source.ErrNotFetched)
	}
	return k.open(dir, l)
}

// checkURL refuses any URL but an https one. The configuration allows ssh
// too, but TrueUp has no ssh credentials to use yet.
func checkURL(raw string) error {
	if problem := config.GitURLProblem(raw); problem != "" {
		return errors.New("the URL " + problem)
	}
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" {
		return errors.New("an ssh repository needs credentials, which TrueUp doesn't support yet: use the repository's https URL")
	}
	return nil
}

func (k *Kind) fetch(ctx context.Context, spec source.Spec) (source.Content, error) {
	dir := source.CacheDir(k.cacheDir, spec.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the source's cache: %w", err)
	}
	start := k.now()
	k.log.Info("fetching source", "source", spec.Name, "url", redactURL(spec.URL), "ref", spec.Ref)
	ref, advertised, err := k.resolve(ctx, spec)
	if err != nil {
		return nil, err
	}

	prev, err := readLatest(dir)
	switch {
	case err == nil && prev.URL == spec.URL && prev.Ref == ref.String() && prev.Advertised == advertised.String() &&
		prev.covers(spec.Limits.MaxFileSize):
		c, err := k.open(dir, prev)
		if err == nil {
			if prev.Requested != spec.Ref {
				prev.Requested = spec.Ref
				if err := writeLatest(dir, prev); err != nil {
					_ = c.Close()
					return nil, err
				}
			}
			k.log.Info("source is unchanged", "source", spec.Name, "version", prev.Version)
			clean(dir, prev.Version, k.now(), k.log)
			return c, nil
		}
		k.log.Warn("the cached clone can't be read, so fetching it again", "source", spec.Name, "error", err)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		k.log.Warn("the source's cache can't be read, so fetching again", "source", spec.Name, "error", err)
	}

	l := latest{URL: spec.URL, Requested: spec.Ref, Ref: ref.String(), Advertised: advertised.String()}
	var written int64
	if l.Version, l.Filter, written, err = k.download(ctx, spec, dir, ref); err != nil {
		return nil, err
	}
	if err := writeLatest(dir, l); err != nil {
		return nil, err
	}
	k.log.Info("fetched source", "source", spec.Name, "version", l.Version, "bytes", written,
		"duration", k.now().Sub(start).Round(time.Millisecond))
	clean(dir, l.Version, k.now(), k.log)
	return k.open(dir, l)
}

// resolve asks the repository which commit the source's branch or tag is
// at. It returns the ref's full name and the hash the repository gives it.
func (k *Kind) resolve(ctx context.Context, spec source.Spec) (plumbing.ReferenceName, plumbing.Hash, error) {
	remote := gogit.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{spec.URL}})
	refs, err := remote.ListContext(ctx, &gogit.ListOptions{ClientOptions: clientOptions(k.http)})
	if err != nil {
		return "", plumbing.Hash{}, fmt.Errorf("list the repository's branches and tags: %w", describe(err))
	}
	return pickRef(refs, spec.Ref)
}

// pickRef finds want among a repository's refs: the branch of that name,
// else the tag, as git clone --branch does. An empty want is the default
// branch, the one HEAD points to.
func pickRef(refs []*plumbing.Reference, want string) (plumbing.ReferenceName, plumbing.Hash, error) {
	byName := make(map[plumbing.ReferenceName]*plumbing.Reference, len(refs))
	for _, r := range refs {
		byName[r.Name()] = r
	}
	var candidates []plumbing.ReferenceName
	if want == "" {
		head, ok := byName[plumbing.HEAD]
		if !ok || head.Type() != plumbing.SymbolicReference || !head.Target().IsBranch() || head.Target().Validate() != nil {
			return "", plumbing.Hash{}, errors.New("the repository doesn't say which branch is its default: set the source's ref")
		}
		candidates = []plumbing.ReferenceName{head.Target()}
	} else {
		candidates = []plumbing.ReferenceName{plumbing.NewBranchReferenceName(want), plumbing.NewTagReferenceName(want)}
	}
	for _, name := range candidates {
		if r, ok := byName[name]; ok && r.Type() == plumbing.HashReference && !r.Hash().IsZero() {
			return name, r.Hash(), nil
		}
	}
	if want == "" {
		return "", plumbing.Hash{}, fmt.Errorf("the repository's default branch, %s, has no commits", cut(shortRef(candidates[0])))
	}
	return "", plumbing.Hash{}, fmt.Errorf("the repository has no branch or tag named %q", want)
}

// download clones the ref into a new directory in the source's cache, and
// installs it as the directory named for its commit. It asks for files no
// larger than max_file_size, and fetches every file from a server that
// can't filter them.
func (k *Kind) download(ctx context.Context, spec source.Spec, dir string, ref plumbing.ReferenceName) (version string, filter, written int64, err error) {
	filter = spec.Limits.MaxFileSize
	for {
		tmp, err := os.MkdirTemp(dir, tmpPrefix)
		if err != nil {
			return "", 0, 0, fmt.Errorf("create a directory for the clone: %w", err)
		}
		version, written, err = k.clone(ctx, spec, tmp, ref, filter)
		if err == nil {
			if err := install(dir, tmp, version); err != nil {
				return "", 0, 0, err
			}
			return version, filter, written, nil
		}
		if rmErr := os.RemoveAll(tmp); rmErr != nil {
			k.log.Warn("can't remove a failed clone from the source cache", "path", tmp, "error", rmErr)
		}
		if filter == 0 || !errors.Is(err, transport.ErrFilterNotSupported) {
			return "", 0, 0, err
		}
		k.log.Warn("the repository's server can't leave out large files, so all are downloaded, up to max_fetch_size",
			"source", spec.Name)
		filter = 0
	}
}

// clone makes a shallow, single-branch, bare clone of the ref in dir, with
// a partial-clone filter at filter bytes unless it is 0, and returns the
// commit it holds and the bytes it wrote.
func (k *Kind) clone(ctx context.Context, spec source.Spec, dir string, ref plumbing.ReferenceName, filter int64) (string, int64, error) {
	q := &quota{limit: spec.Limits.MaxFetchSize, maxObjects: k.maxObjects}
	// osfs confines every path to dir, through os.Root.
	st := filesystem.NewStorage(quotaFS{Filesystem: osfs.New(dir), q: q}, cache.NewObjectLRUDefault())
	opts := &gogit.CloneOptions{
		URL:           spec.URL,
		ClientOptions: clientOptions(k.http),
		ReferenceName: ref,
		SingleBranch:  true,
		NoCheckout:    true,
		Depth:         1,
		Tags:          plumbing.NoTags,
		Bare:          true,
	}
	if filter > 0 {
		// The filter leaves out blobs of at least the size it names, so
		// one byte more keeps a file of exactly max_file_size.
		opts.Filter = packp.FilterBlobLimit(uint64(filter)+1, packp.BlobLimitPrefixNone)
	}
	repo, err := gogit.CloneContext(ctx, st, nil, opts)
	if err != nil {
		_ = st.Close()
		if qErr := q.err(); errors.Is(qErr, errTooLarge) {
			return "", 0, fmt.Errorf("%w, %d bytes: raise it, or narrow max_file_size", qErr, spec.Limits.MaxFetchSize)
		} else if qErr != nil {
			return "", 0, qErr
		}
		return "", 0, fmt.Errorf("clone %s: %w", shortRef(ref), describe(err))
	}
	version, err := commitOf(repo, ref)
	if closeErr := repo.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close the clone: %w", closeErr)
	}
	return version, q.bytes(), err
}

// commitOf returns the commit a clone's ref points to, through any tags,
// once it has checked that the commit and its tree are there.
func commitOf(repo *gogit.Repository, name plumbing.ReferenceName) (string, error) {
	ref, err := repo.Reference(name, true)
	if err != nil {
		return "", fmt.Errorf("find %s in the clone: %w", shortRef(name), err)
	}
	h := ref.Hash()
	for range 8 {
		obj, err := repo.Object(plumbing.AnyObject, h)
		if err != nil {
			return "", fmt.Errorf("read object %s: %w", h, err)
		}
		switch o := obj.(type) {
		case *object.Commit:
			if _, err := o.Tree(); err != nil {
				return "", fmt.Errorf("read the tree of commit %s: %w", h, err)
			}
			return o.Hash.String(), nil
		case *object.Tag:
			h = o.Target
		default:
			return "", fmt.Errorf("%s points to a %s, not a commit", shortRef(name), obj.Type())
		}
	}
	return "", fmt.Errorf("%s is a tag of a tag, too many times over", shortRef(name))
}

// open opens the clone l names, in a source's cache.
func (k *Kind) open(dir string, l latest) (*content, error) {
	st := filesystem.NewStorage(osfs.New(filepath.Join(dir, l.Version)), cache.NewObjectLRUDefault())
	repo, err := gogit.Open(st, nil)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("open the cached clone of %s: %w", l.Version, err)
	}
	h, err := parseHash(l.Version)
	if err != nil {
		_ = repo.Close()
		return nil, err
	}
	commit, err := repo.CommitObject(h)
	if err != nil {
		_ = repo.Close()
		return nil, fmt.Errorf("read commit %s from the cache: %w", l.Version, err)
	}
	return &content{
		repo:     repo,
		version:  l.Version,
		ref:      shortRef(plumbing.ReferenceName(l.Ref)),
		root:     commit.TreeHash.String(),
		filtered: l.Filter > 0,
	}, nil
}

// describe adds what a person can do to go-git's errors where it can, and
// cuts the text of the rest short, since it can hold what the server sent.
func describe(err error) error {
	switch {
	case errors.Is(err, transport.ErrRepositoryNotFound),
		errors.Is(err, transport.ErrAuthenticationRequired),
		errors.Is(err, transport.ErrAuthorizationFailed):
		return fmt.Errorf("the repository wasn't found, or needs credentials, which TrueUp doesn't support yet: %w", cutError{err})
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		return fmt.Errorf("the repository is empty: %w", cutError{err})
	}
	return cutError{err}
}

// maxErrorText is the most of go-git's error text that reaches TrueUp's
// errors.
const maxErrorText = 500

// cutError is an error whose text is cut short.
type cutError struct{ err error }

func (e cutError) Error() string { return cut(e.err.Error()) }
func (e cutError) Unwrap() error { return e.err }

// cut cuts text to maxErrorText bytes, on a character boundary.
func cut(s string) string {
	if len(s) <= maxErrorText {
		return s
	}
	n := maxErrorText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
