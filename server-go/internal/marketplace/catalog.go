package marketplace

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"sync"
)

//go:embed templates
var templatesFS embed.FS

// ErrNotFound is returned when a slug has no template.
var ErrNotFound = errors.New("marketplace: app not found")

// Entry is one fully-loaded catalog app.
type Entry struct {
	Manifest     *Manifest
	TemplateYAML []byte
	Icon         []byte
}

var (
	loadOnce sync.Once
	loaded   map[string]*Entry
	loadErr  error
)

func load() {
	loaded = map[string]*Entry{}
	dirs, err := templatesFS.ReadDir("templates")
	if err != nil {
		loadErr = err
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		slug := d.Name()
		base := path.Join("templates", slug)
		mraw, err := fs.ReadFile(templatesFS, path.Join(base, "manifest.yaml"))
		if err != nil {
			loadErr = fmt.Errorf("%s: read manifest: %w", slug, err)
			return
		}
		m, err := ParseManifest(mraw)
		if err != nil {
			loadErr = fmt.Errorf("%s: %w", slug, err)
			return
		}
		if m.Name != slug {
			loadErr = fmt.Errorf("%s: manifest name %q must equal directory name", slug, m.Name)
			return
		}
		tmpl, err := fs.ReadFile(templatesFS, path.Join(base, "kuso.yaml"))
		if err != nil {
			loadErr = fmt.Errorf("%s: read kuso.yaml: %w", slug, err)
			return
		}
		icon, _ := fs.ReadFile(templatesFS, path.Join(base, "icon.svg")) // optional
		loaded[slug] = &Entry{Manifest: m, TemplateYAML: tmpl, Icon: icon}
	}
}

// Catalog returns all app manifests, sorted by title.
func Catalog() ([]*Manifest, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	out := make([]*Manifest, 0, len(loaded))
	for _, e := range loaded {
		out = append(out, e.Manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out, nil
}

// GetEntry returns one loaded template by slug.
func GetEntry(slug string) (*Entry, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	e, ok := loaded[slug]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return e, nil
}

// Validate loads every template and confirms it parses + renders with
// stub required answers. Used by the CI test and can be called at boot
// as a fail-fast.
func Validate() error {
	_, err := Catalog()
	return err
}
