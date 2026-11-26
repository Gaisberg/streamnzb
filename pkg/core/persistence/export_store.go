package persistence

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The export store is the writable half of the NFS export: a directory tree
// of names the client chose, persisted here, whose files are aliases to
// library rows. Radarr and Sonarr import a completed download by renaming it
// into their library folder, so that folder has to be somewhere they can
// rename things; this is that somewhere. A rename or a move here is a row
// update — no media moves, because no media is stored.
//
// One folder is fixed at the top of the tree: "downloads", where the download
// client API publishes finished jobs. Everything else — the *arr's root
// folders, say media/movies and media/tv — the client creates itself.

const (
	ExportKindDir  = "dir"
	ExportKindFile = "file"

	ExportRootDownloads = "downloads"
	// ExportReservedLibrary is the generated, read-only folder the NFS
	// export shows beside the client's own; a client cannot create it.
	ExportReservedLibrary = "library"
)

var (
	ErrExportExists   = errors.New("export: name already exists")
	ErrExportNotFound = errors.New("export: no such node")
	ErrExportNotEmpty = errors.New("export: directory not empty")
	ErrExportRoot     = errors.New("export: the downloads folder is fixed")
	ErrExportBadName  = errors.New("export: invalid name")
	ErrExportTooLarge = errors.New("export: file too large")
	ErrExportNotFile  = errors.New("export: not a writable file")
)

// MaxScratchBytes caps a scratch file. Subtitles run to a few hundred
// kilobytes; nothing a client should write here is media.
const MaxScratchBytes = 8 << 20

// ExportNode is one directory, file alias or scratch file. ParentID 0 is the
// tree root. A file with an ItemID aliases a library row; one without is a
// scratch file whose content is Data.
type ExportNode struct {
	ID         int64
	ParentID   int64
	Name       string
	Kind       string
	ItemID     string
	Data       []byte
	CreatedAt  time.Time
	ModifiedAt time.Time
}

func (n ExportNode) IsDir() bool     { return n.Kind == ExportKindDir }
func (n ExportNode) IsScratch() bool { return n.Kind == ExportKindFile && n.ItemID == "" }

type exportQueries interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

type exportDB struct {
	db  exportQueries
	wdb exportQueries
}

type ExportStore struct {
	exportDB
	writer *connRef
	mu     sync.Mutex
}

func NewExportStore(db, wdb *connRef) *ExportStore {
	if db == nil || wdb == nil {
		return &ExportStore{}
	}
	return &ExportStore{exportDB: exportDB{db: db, wdb: wdb}, writer: wdb}
}

func validExportName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || len(name) > 255 {
		return ErrExportBadName
	}
	return nil
}

// EnsureRoots creates the fixed downloads folder if it is missing.
func (es *ExportStore) EnsureRoots() error {
	if es == nil || es.db == nil {
		return nil
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	if _, ok, err := es.childLocked(0, ExportRootDownloads); err != nil {
		return err
	} else if !ok {
		if _, err := es.insertLocked(0, ExportRootDownloads, ExportKindDir, ""); err != nil {
			return err
		}
	}
	return nil
}

// isFixed reports the one node a client may not rename or remove.
func isFixed(n ExportNode) bool {
	return n.ParentID == 0 && n.Name == ExportRootDownloads
}

// Nodes returns the whole tree, roots first by id.
func (es *ExportStore) Nodes() ([]ExportNode, error) {
	if es == nil || es.db == nil {
		return nil, nil
	}
	rows, err := es.db.Query(`SELECT id, parent_id, name, kind, COALESCE(item_id, ''), data, created_at, modified_at FROM export_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportNode
	for rows.Next() {
		var n ExportNode
		var created, modified int64
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Name, &n.Kind, &n.ItemID, &n.Data, &created, &modified); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(created, 0)
		n.ModifiedAt = time.Unix(modified, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

// Child finds the entry called name under parent.
func (es *ExportStore) Child(parent int64, name string) (ExportNode, bool, error) {
	if es == nil || es.db == nil {
		return ExportNode{}, false, nil
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.childLocked(parent, name)
}

func (es *exportDB) childLocked(parent int64, name string) (ExportNode, bool, error) {
	var n ExportNode
	var created, modified int64
	err := es.db.QueryRow(`SELECT id, parent_id, name, kind, COALESCE(item_id, ''), data, created_at, modified_at FROM export_nodes WHERE parent_id = ? AND name = ?`, parent, name).
		Scan(&n.ID, &n.ParentID, &n.Name, &n.Kind, &n.ItemID, &n.Data, &created, &modified)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportNode{}, false, nil
	}
	if err != nil {
		return ExportNode{}, false, err
	}
	n.CreatedAt = time.Unix(created, 0)
	n.ModifiedAt = time.Unix(modified, 0)
	return n, true, nil
}

func (es *exportDB) getLocked(id int64) (ExportNode, error) {
	var n ExportNode
	var created, modified int64
	err := es.db.QueryRow(`SELECT id, parent_id, name, kind, COALESCE(item_id, ''), data, created_at, modified_at FROM export_nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.ParentID, &n.Name, &n.Kind, &n.ItemID, &n.Data, &created, &modified)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportNode{}, ErrExportNotFound
	}
	if err != nil {
		return ExportNode{}, err
	}
	n.CreatedAt = time.Unix(created, 0)
	n.ModifiedAt = time.Unix(modified, 0)
	return n, nil
}

func (es *exportDB) insertLocked(parent int64, name, kind, itemID string) (ExportNode, error) {
	now := time.Now()
	// Ids are handed out here rather than by the database: the tree refers
	// to itself by id, and a backend migration must be able to carry the
	// ids across, which a generated identity column would refuse. They are
	// never reused — an NFS client keeps the handle of a deleted file and
	// must get ESTALE, not somebody else's file — so the next id is at least
	// the current microsecond, which no deleted row can have reached.
	var maxID int64
	if err := es.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM export_nodes`).Scan(&maxID); err != nil {
		return ExportNode{}, err
	}
	id := max(maxID+1, now.UnixMicro())
	if _, err := es.wdb.Exec(`INSERT INTO export_nodes (id, parent_id, name, kind, item_id, created_at, modified_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, parent, name, kind, itemID, now.Unix(), now.Unix()); err != nil {
		return ExportNode{}, err
	}
	es.touchLocked(parent, now)
	return ExportNode{ID: id, ParentID: parent, Name: name, Kind: kind, ItemID: itemID, CreatedAt: now, ModifiedAt: now}, nil
}

func (es *exportDB) touchLocked(id int64, now time.Time) {
	if id > 0 {
		_, _ = es.wdb.Exec(`UPDATE export_nodes SET modified_at = ? WHERE id = ?`, now.Unix(), id)
	}
}

func (es *exportDB) hasChildrenLocked(id int64) (bool, error) {
	var n int
	if err := es.db.QueryRow(`SELECT COUNT(*) FROM export_nodes WHERE parent_id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// Mkdir creates a directory under parent.
func (es *ExportStore) Mkdir(parent int64, name string) (ExportNode, error) {
	if es == nil || es.db == nil {
		return ExportNode{}, errors.New("export store unavailable")
	}
	if err := validExportName(name); err != nil {
		return ExportNode{}, err
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	if parent != 0 {
		if p, err := es.getLocked(parent); err != nil {
			return ExportNode{}, err
		} else if !p.IsDir() {
			return ExportNode{}, fmt.Errorf("export: parent is not a directory")
		}
	}
	if parent == 0 && name == ExportReservedLibrary {
		return ExportNode{}, ErrExportExists
	}
	if _, exists, err := es.childLocked(parent, name); err != nil {
		return ExportNode{}, err
	} else if exists {
		return ExportNode{}, ErrExportExists
	}
	return es.insertLocked(parent, name, ExportKindDir, "")
}

// AddFile creates a file alias to a library row under parent. A file of the
// same name is replaced: a republished job points at the newer row.
func (es *ExportStore) AddFile(parent int64, name, itemID string) (ExportNode, error) {
	if es == nil || es.db == nil {
		return ExportNode{}, errors.New("export store unavailable")
	}
	if err := validExportName(name); err != nil {
		return ExportNode{}, err
	}
	if strings.TrimSpace(itemID) == "" {
		return ExportNode{}, errors.New("export: file needs a library row")
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	var added ExportNode
	err := es.writer.withTx(func(tx *txn) error {
		store := exportDB{db: tx, wdb: tx}
		var err error
		added, err = store.addFileLocked(parent, name, itemID)
		return err
	})
	if err != nil {
		return ExportNode{}, err
	}
	return added, nil
}

func (es *exportDB) addFileLocked(parent int64, name, itemID string) (ExportNode, error) {
	if parent != 0 {
		p, err := es.getLocked(parent)
		if err != nil {
			return ExportNode{}, err
		}
		if !p.IsDir() {
			return ExportNode{}, fmt.Errorf("export: parent is not a directory")
		}
	}
	if existing, ok, err := es.childLocked(parent, name); err != nil {
		return ExportNode{}, err
	} else if ok {
		if existing.IsDir() {
			return ExportNode{}, ErrExportExists
		}
		if _, err := es.wdb.Exec(`DELETE FROM export_nodes WHERE id = ?`, existing.ID); err != nil {
			return ExportNode{}, err
		}
	}
	return es.insertLocked(parent, name, ExportKindFile, itemID)
}

// AddScratch creates an empty scratch file under parent: a file the client
// writes itself, kept in Data. Replacing an alias of the same name is
// refused — a release is not something a client overwrites.
func (es *ExportStore) AddScratch(parent int64, name string) (ExportNode, error) {
	if es == nil || es.db == nil {
		return ExportNode{}, errors.New("export store unavailable")
	}
	if err := validExportName(name); err != nil {
		return ExportNode{}, err
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	if parent != 0 {
		p, err := es.getLocked(parent)
		if err != nil {
			return ExportNode{}, err
		}
		if !p.IsDir() {
			return ExportNode{}, fmt.Errorf("export: parent is not a directory")
		}
	}
	if _, exists, err := es.childLocked(parent, name); err != nil {
		return ExportNode{}, err
	} else if exists {
		return ExportNode{}, ErrExportExists
	}
	return es.insertLocked(parent, name, ExportKindFile, "")
}

// WriteScratch writes data at offset into a scratch file, extending it with
// zeros as needed. A nil data sets the file's length to offset (a truncate
// or zero-extend). Aliases and directories refuse.
func (es *ExportStore) WriteScratch(id int64, offset uint64, data []byte) error {
	if es == nil || es.db == nil {
		return errors.New("export store unavailable")
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	n, err := es.getLocked(id)
	if err != nil {
		return err
	}
	if !n.IsScratch() {
		return ErrExportNotFile
	}
	end := offset + uint64(len(data))
	if end > MaxScratchBytes {
		return ErrExportTooLarge
	}
	buf := n.Data
	if data == nil {
		// Set length.
		if uint64(len(buf)) > offset {
			buf = buf[:offset]
		} else {
			buf = append(buf, make([]byte, int(offset)-len(buf))...)
		}
	} else {
		if uint64(len(buf)) < end {
			buf = append(buf, make([]byte, int(end)-len(buf))...)
		}
		copy(buf[offset:end], data)
	}
	now := time.Now()
	if _, err := es.wdb.Exec(`UPDATE export_nodes SET data = ?, modified_at = ? WHERE id = ?`, buf, now.Unix(), id); err != nil {
		return err
	}
	return nil
}

// Rename moves a node to a new parent and name (0 is the root). An existing
// file at the target is replaced; an existing empty directory is replaced
// when the source is a directory; a non-empty one refuses. The downloads
// folder cannot be moved. A directory cannot be moved into itself.
func (es *ExportStore) Rename(id, newParent int64, newName string) error {
	if es == nil || es.db == nil {
		return errors.New("export store unavailable")
	}
	if err := validExportName(newName); err != nil {
		return err
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.writer.withTx(func(tx *txn) error {
		store := exportDB{db: tx, wdb: tx}
		return store.renameLocked(id, newParent, newName)
	})
}

func (es *exportDB) renameLocked(id, newParent int64, newName string) error {
	n, err := es.getLocked(id)
	if err != nil {
		return err
	}
	if isFixed(n) {
		return ErrExportRoot
	}
	if newParent != 0 {
		p, err := es.getLocked(newParent)
		if err != nil {
			return err
		}
		if !p.IsDir() {
			return fmt.Errorf("export: target parent is not a directory")
		}
		// Walk up from the target parent; landing on the source means the
		// move would put a directory inside itself.
		for cur := p; ; {
			if cur.ID == id {
				return fmt.Errorf("export: cannot move a directory into itself")
			}
			if cur.ParentID == 0 {
				break
			}
			if cur, err = es.getLocked(cur.ParentID); err != nil {
				return err
			}
		}
	}
	if newParent == 0 && newName == ExportReservedLibrary {
		return ErrExportExists
	}
	if target, exists, err := es.childLocked(newParent, newName); err != nil {
		return err
	} else if exists && target.ID != id {
		if isFixed(target) {
			return ErrExportRoot
		}
		if target.IsDir() {
			if !n.IsDir() {
				return ErrExportExists
			}
			if has, err := es.hasChildrenLocked(target.ID); err != nil {
				return err
			} else if has {
				return ErrExportNotEmpty
			}
		} else if n.IsDir() {
			return ErrExportExists
		}
		if _, err := es.wdb.Exec(`DELETE FROM export_nodes WHERE id = ?`, target.ID); err != nil {
			return err
		}
	}
	now := time.Now()
	if _, err := es.wdb.Exec(`UPDATE export_nodes SET parent_id = ?, name = ?, modified_at = ? WHERE id = ?`, newParent, newName, now.Unix(), id); err != nil {
		return err
	}
	es.touchLocked(n.ParentID, now)
	es.touchLocked(newParent, now)
	return nil
}

// RemoveTree removes a directory containing only dangling aliases. The export
// calls it for a directory the client sees as empty: its remaining entries
// are aliases of rows that no longer exist, hidden from the listing but
// still rows here, and an RMDIR must not fail on ghosts.
func (es *ExportStore) RemoveTree(id int64) error {
	if es == nil || es.db == nil {
		return errors.New("export store unavailable")
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	n, err := es.getLocked(id)
	if err != nil {
		return err
	}
	if isFixed(n) {
		return ErrExportRoot
	}
	return es.writer.withTx(func(tx *txn) error {
		if _, err := tx.Exec(`DELETE FROM export_nodes WHERE parent_id = ? AND kind = ? AND item_id <> '' AND NOT EXISTS (SELECT 1 FROM library_nzbs WHERE library_nzbs.id = export_nodes.item_id)`, id, ExportKindFile); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM export_nodes WHERE id = ? AND kind = ? AND NOT EXISTS (SELECT 1 FROM export_nodes children WHERE children.parent_id = ?)`, id, ExportKindDir, id)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrExportNotEmpty
		}
		_, err = tx.Exec(`UPDATE export_nodes SET modified_at = ? WHERE id = ?`, time.Now().Unix(), n.ParentID)
		return err
	})
}

// Remove deletes a file alias or an empty directory. The library row an
// alias pointed at is untouched: the *arr's view of its files and the
// library's record of validated releases are different things.
func (es *ExportStore) Remove(id int64) error {
	if es == nil || es.db == nil {
		return errors.New("export store unavailable")
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	n, err := es.getLocked(id)
	if err != nil {
		return err
	}
	if isFixed(n) {
		return ErrExportRoot
	}
	if n.IsDir() {
		if has, err := es.hasChildrenLocked(id); err != nil {
			return err
		} else if has {
			return ErrExportNotEmpty
		}
	}
	if _, err := es.wdb.Exec(`DELETE FROM export_nodes WHERE id = ?`, id); err != nil {
		return err
	}
	es.touchLocked(n.ParentID, time.Now())
	return nil
}
