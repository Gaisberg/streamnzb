package speedtest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/nzb"
)

// Article is one message-id the benchmark may download.
type Article struct {
	ID    string
	Bytes int64
}

// Corpus is the pool of articles a run pulls from, with a human label naming
// where it came from (the report shows it, since article age and origin change
// what the numbers mean).
type Corpus struct {
	Label    string
	Articles []Article
}

// ArticleSource supplies the corpus. It is an interface so the tester stays
// free of HTTP and database concerns: the public-NZB source lives here, the
// library source is wired in by the server.
type ArticleSource interface {
	Corpus(ctx context.Context) (*Corpus, error)
}

// corpusFromNZB flattens an NZB into a work list, largest file first.
//
// PAR2 volumes are dropped: they are recovery padding that many posts never
// need, so they are the articles most likely to be missing on a given provider
// — a 430 storm would measure nothing but error handling.
func corpusFromNZB(n *nzb.NZB, label string) *Corpus {
	if n == nil {
		return nil
	}
	type sized struct {
		file  *nzb.File
		bytes int64
	}
	files := make([]sized, 0, len(n.Files))
	for i := range n.Files {
		file := &n.Files[i]
		if isPar2(file.Subject) {
			continue
		}
		var total int64
		for _, segment := range file.Segments {
			total += segment.Bytes
		}
		if total <= 0 {
			continue
		}
		files = append(files, sized{file: file, bytes: total})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].bytes > files[j].bytes })

	corpus := &Corpus{Label: label}
	for _, entry := range files {
		for _, segment := range entry.file.Segments {
			id := strings.TrimSpace(segment.ID)
			if id == "" {
				continue
			}
			corpus.Articles = append(corpus.Articles, Article{ID: id, Bytes: segment.Bytes})
		}
	}
	if len(corpus.Articles) == 0 {
		return nil
	}
	return corpus
}

func isPar2(subject string) bool {
	return strings.Contains(strings.ToLower(subject), ".par2")
}

// publicNZBCache holds parsed test NZBs keyed by URL, so repeat runs do not
// re-fetch the host serving the fixed test post.
var publicNZBCache sync.Map // url -> *Corpus

// PublicNZBSource downloads the fixed public test NZB. A known post keeps
// results comparable between providers and between installs, which a release
// out of the user's own library can never be — every provider holds a different
// copy of it, at a different age.
type PublicNZBSource struct {
	// URL overrides the configured test NZB. Empty uses env.SpeedTestNZBURL.
	URL string
}

func (s PublicNZBSource) Corpus(ctx context.Context) (*Corpus, error) {
	url := strings.TrimSpace(s.URL)
	if url == "" {
		url = env.SpeedTestNZBURL()
	}
	if cached, ok := publicNZBCache.Load(url); ok {
		return cached.(*Corpus), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("test nzb request: %w", err)
	}
	req.Header.Set("User-Agent", env.DefaultIndexerUserAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch test nzb: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch test nzb: %s", resp.Status)
	}

	const maxNZBBytes = 16 << 20
	parsed, err := nzb.Parse(io.LimitReader(resp.Body, maxNZBBytes))
	if err != nil {
		return nil, fmt.Errorf("parse test nzb: %w", err)
	}
	corpus := corpusFromNZB(parsed, "Public test NZB")
	if corpus == nil {
		return nil, fmt.Errorf("test nzb %s contains no usable articles", url)
	}
	logger.Debug("Speed test NZB loaded", "url", url, "articles", len(corpus.Articles))
	publicNZBCache.Store(url, corpus)
	return corpus, nil
}

// StaticSource serves an already-parsed NZB, used for the library corpus.
type StaticSource struct {
	NZB   *nzb.NZB
	Label string
}

func (s StaticSource) Corpus(ctx context.Context) (*Corpus, error) {
	corpus := corpusFromNZB(s.NZB, s.Label)
	if corpus == nil {
		return nil, fmt.Errorf("release %q contains no usable articles", s.Label)
	}
	return corpus, nil
}
