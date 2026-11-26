package nfs

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/search/parser"
)

// The library folder is the export's view of releases nobody organises: rows
// that reached the library by being played, and have no alias anywhere in
// the writable tree. Radarr and Sonarr never see them, so the export lays
// them out the way a media scanner wants, read-only:
//
//	library/
//	  Movies/
//	    Title (Year) {imdb-tt0111161}/
//	      Title (Year) - Release.Name.2160p.mkv      ← one file per stored release
//	  Shows/
//	    Title (Year) {tvdb-81189}/
//	      Season 01/
//	        Title (Year) - s01e02 - Release.Name.mkv
//
// The id tag in the folder name is what makes the match deterministic: the
// scanner reads it before it guesses from the title. Several releases of one
// title sit in one folder and differ after the " - ", which Plex reads as
// versions of a single item. A row the *arr imports gains an alias in its
// folder and drops out of here, so nothing is listed twice.

const (
	libraryDirName = "library"
	moviesDirName  = "Movies"
	showsDirName   = "Shows"
)

// titleFunc names the title a library row belongs to; ok false means the
// caller should fall back to the release name.
type titleFunc func(item *persistence.LibraryItem) (name string, year int, ok bool)

// addLibraryTree grafts library/ onto the root from the rows not in aliased.
func addLibraryTree(t *tree, root *node, items []*persistence.LibraryItem, aliased map[string]bool, titles titleFunc, gen interface{ Write([]byte) (int, error) }) {
	lib := newGeneratedDir(root, libraryDirName)
	movies := newGeneratedDir(lib, moviesDirName)
	shows := newGeneratedDir(lib, showsDirName)
	t.nodes[lib.id] = lib
	t.nodes[movies.id] = movies
	t.nodes[shows.id] = shows

	// Keyed case-insensitively: two releases of one title spelled "House Of
	// The Dragon" and "House of the Dragon" belong in one folder, and a
	// scanner on a case-insensitive client would see them as one anyway.
	dirs := map[string]*node{}
	dirFor := func(parent *node, name string) *node {
		key := strings.ToLower(path.Join(parent.path, name))
		if d, ok := dirs[key]; ok {
			return d
		}
		d := newGeneratedDir(parent, name)
		dirs[key] = d
		t.nodes[d.id] = d
		return d
	}

	for _, item := range items {
		if item == nil || item.ID == "" || aliased[item.ID] {
			continue
		}
		title, year := titleFor(item, titles)
		folder := titleFolderName(title, year, idTag(item))
		stem := titleStem(title, year)
		ext := mediaExt(item.MediaFileName)
		var dir *node
		var name string
		if isMovieLike(item) {
			dir = dirFor(movies, folder)
			name = sanitizeName(stem+" - "+versionLabel(item.ReleaseTitle), maxNameBytes-len(ext)) + ext
		} else {
			show := dirFor(shows, folder)
			dir = dirFor(show, fmt.Sprintf("Season %02d", item.Season))
			name = sanitizeName(fmt.Sprintf("%s - s%02de%02d - %s", stem, item.Season, item.Episode, versionLabel(item.ReleaseTitle)), maxNameBytes-len(ext)) + ext
		}
		name = uniqueName(dir, name, ext)
		f := &node{kind: kindFile, name: name, path: path.Join(dir.path, name), item: item, size: fileSize(item), mtime: item.CreatedAt}
		// Keyed by the row, not the path: a title lookup that starts
		// answering later renames the file but must not strand a handle a
		// client already holds.
		f.id = nodeIDFor(kindFile, item.ID)
		dir.addChild(f)
		t.nodes[f.id] = f
		_, _ = gen.Write([]byte(f.path))
		_, _ = gen.Write([]byte{0})
	}
}

// newGeneratedDir makes a read-only directory under parent, keyed by path.
func newGeneratedDir(parent *node, name string) *node {
	full := path.Join(parent.path, name)
	d := &node{kind: kindDir, name: name, path: full, id: nodeIDFor(kindDir, full)}
	parent.addChild(d)
	return d
}

func isMovieLike(item *persistence.LibraryItem) bool {
	return item.Season == 0 && item.Episode == 0
}

// titleFor names the row's title: the metadata answer when there is one,
// otherwise what the release name parses to, otherwise the release name.
func titleFor(item *persistence.LibraryItem, titles titleFunc) (string, int) {
	if titles != nil {
		if name, year, ok := titles(item); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name), year
		}
	}
	if parsed := parser.ParseReleaseTitle(item.ReleaseTitle); parsed != nil && parsed.Result != nil && strings.TrimSpace(parsed.Title) != "" {
		return strings.TrimSpace(parsed.Title), parsed.Year
	}
	return strings.TrimSpace(item.ReleaseTitle), 0
}

// idTag is the scanner hint for the folder: Plex, Emby and Jellyfin all read
// {imdb-…}, {tmdb-…} and {tvdb-…}. Kitsu has no such tag.
func idTag(item *persistence.LibraryItem) string {
	switch {
	case strings.HasPrefix(item.ImdbID, "tt"):
		return "{imdb-" + item.ImdbID + "}"
	case item.TmdbID != "":
		return "{tmdb-" + item.TmdbID + "}"
	case item.TvdbID != "":
		return "{tvdb-" + item.TvdbID + "}"
	case strings.HasPrefix(item.ContentID, "tt"):
		return "{imdb-" + item.ContentID + "}"
	}
	return ""
}

func titleStem(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

func titleFolderName(title string, year int, tag string) string {
	name := titleStem(title, year)
	if tag != "" {
		name += " " + tag
	}
	return sanitizeName(name, maxNameBytes)
}

// versionLabel is what distinguishes one release of a title from another in
// the file name: the release name as posted.
func versionLabel(releaseTitle string) string {
	label := strings.TrimSpace(releaseTitle)
	if label == "" {
		return "release"
	}
	return label
}

func mediaExt(mediaFileName string) string {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(mediaFileName)))
	if ext == "" || len(ext) > 8 {
		return defaultMediaExt
	}
	return ext
}

// uniqueName keeps two rows from producing one file name. It only happens
// when sanitising collapsed a difference; the suffix keeps both reachable.
func uniqueName(dir *node, name, ext string) string {
	if dir.child(name) == nil {
		return name
	}
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		candidate := base + " [" + strconv.Itoa(i) + "]" + ext
		if dir.child(candidate) == nil {
			return candidate
		}
	}
}
