package extract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
	kzstd "github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

// CheckpointRow is a joined checkpoint + asset row.
type CheckpointRow struct {
	Id           string `db:"id"`
	CreatedAt    string `db:"created_at"`
	AssetId      string `db:"asset_id"`
	FileSize     int64  `db:"file_size"`
	Chunks       string `db:"chunks"`
	GroupId      string `db:"group_id"`
	TimeModified int64  `db:"time_modified"`
	AssetName    string `db:"asset_name"`
	Extension    string `db:"extension"`
	CollPath     string `db:"coll_path"`
}

// FileOp is a single file in a commit group.
type FileOp struct {
	RelPath   string // relative path, e.g. "Key Animation/Dusty.blend"
	TempPath  string // absolute path after extraction
	Operation string // "add" or "modify"
	FileSize  int64
}

// CommitGroup is one checkpoint group to replay.
type CommitGroup struct {
	Index     int
	GroupId   string
	Files     []FileOp
	Timestamp string
}

// SaveTimeline writes commit groups to JSON.
func SaveTimeline(groups []CommitGroup, path string) error {
	data, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadTimeline reads a saved timeline and rebuilds TempPaths.
func LoadTimeline(path string, stagingDir string) ([]CommitGroup, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var groups []CommitGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, err
	}
	for i := range groups {
		commitDir := filepath.Join(stagingDir, fmt.Sprintf("%04d", groups[i].Index))
		for j := range groups[i].Files {
			groups[i].Files[j].TempPath = filepath.Join(commitDir, groups[i].Files[j].RelPath)
		}
	}
	return groups, nil
}

// ExtractTimeline returns all commit groups (including those with missing chunks).
func ExtractTimeline(clstPath string) ([]CommitGroup, error) {
	return extractTimelineFiltered(clstPath, false)
}

// ExtractAvailableTimeline returns only commit groups with all chunks present.
func ExtractAvailableTimeline(clstPath string) ([]CommitGroup, error) {
	return extractTimelineFiltered(clstPath, true)
}

func extractTimelineFiltered(clstPath string, filterMissing bool) ([]CommitGroup, error) {
	db, err := sqlx.Open("sqlite3", clstPath+"?_journal=WAL&mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	var rows []CheckpointRow
	err = db.Select(&rows, `
		SELECT 
			ac.id, ac.created_at, ac.asset_id, ac.file_size, ac.chunks, 
			ac.group_id, ac.time_modified,
			a.name AS asset_name, a.extension,
			IFNULL(c.collection_path, '/') AS coll_path
		FROM asset_checkpoint ac
		JOIN asset a ON ac.asset_id = a.id
		LEFT JOIN collection c ON a.collection_id = c.id
		WHERE ac.trashed = 0
		ORDER BY ac.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query checkpoints: %w", err)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("no checkpoints found in %s", clstPath)
	}

	var localChunks map[string]bool
	if filterMissing {
		var allChunkHashes []string
		err = db.Select(&allChunkHashes, "SELECT hash FROM chunk")
		if err != nil {
			return nil, fmt.Errorf("query chunk hashes: %w", err)
		}
		localChunks = make(map[string]bool, len(allChunkHashes))
		for _, h := range allChunkHashes {
			localChunks[h] = true
		}

		var filtered []CheckpointRow
		for _, r := range rows {
			hashes := strings.Split(r.Chunks, ",")
			allPresent := true
			for _, h := range hashes {
				if h == "" {
					continue
				}
				if !localChunks[h] {
					allPresent = false
					break
				}
			}
			if allPresent {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}

	type groupEntry struct {
		minTime string
		rows    []CheckpointRow
	}
	groupMap := make(map[string]*groupEntry)
	var groupOrder []string
	emptyIdx := 0

	for _, r := range rows {
		gid := r.GroupId
		if gid == "" {
			gid = fmt.Sprintf("__solo_%d", emptyIdx)
			emptyIdx++
		}
		if _, ok := groupMap[gid]; !ok {
			groupMap[gid] = &groupEntry{minTime: r.CreatedAt}
			groupOrder = append(groupOrder, gid)
		}
		g := groupMap[gid]
		if r.CreatedAt < g.minTime {
			g.minTime = r.CreatedAt
		}
		g.rows = append(g.rows, r)
	}

	sort.Slice(groupOrder, func(i, j int) bool {
		return groupMap[groupOrder[i]].minTime < groupMap[groupOrder[j]].minTime
	})

	seenAssets := make(map[string]bool)

	var groups []CommitGroup
	for idx, gid := range groupOrder {
		ge := groupMap[gid]
		cg := CommitGroup{
			Index:     idx + 1,
			GroupId:   gid,
			Timestamp: ge.minTime,
		}

		for _, r := range ge.rows {
			relDir := strings.Trim(r.CollPath, "/")
			fileName := r.AssetName + "." + r.Extension
			var relPath string
			if relDir == "" {
				relPath = fileName
			} else {
				relPath = filepath.Join(relDir, fileName)
			}

			op := "modify"
			if !seenAssets[r.AssetId] {
				op = "add"
				seenAssets[r.AssetId] = true
			}

			cg.Files = append(cg.Files, FileOp{
				RelPath:   relPath,
				Operation: op,
				FileSize:  r.FileSize,
			})
		}
		groups = append(groups, cg)
	}
	return groups, nil
}

// StageCommitFiles reconstructs files for a commit group from .clst chunks.
func StageCommitFiles(clstPath string, group *CommitGroup, stagingDir string) error {
	db, err := sqlx.Open("sqlite3", clstPath+"?_journal=WAL&mode=ro")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	commitDir := filepath.Join(stagingDir, fmt.Sprintf("%04d", group.Index))
	os.MkdirAll(commitDir, 0755)

	gid := group.GroupId
	var checkpoints []CheckpointRow

	if strings.HasPrefix(gid, "__solo_") {
		err = db.Select(&checkpoints, `
			SELECT ac.id, ac.chunks, ac.file_size, ac.time_modified,
				a.name AS asset_name, a.extension,
				IFNULL(c.collection_path, '/') AS coll_path
			FROM asset_checkpoint ac
			JOIN asset a ON ac.asset_id = a.id
			LEFT JOIN collection c ON a.collection_id = c.id
			WHERE ac.trashed = 0 AND (ac.group_id = '' OR ac.group_id IS NULL)
			ORDER BY ac.created_at ASC
		`)
		if err != nil {
			return fmt.Errorf("query solo checkpoints: %w", err)
		}
	} else {
		err = db.Select(&checkpoints, `
			SELECT ac.id, ac.chunks, ac.file_size, ac.time_modified,
				a.name AS asset_name, a.extension,
				IFNULL(c.collection_path, '/') AS coll_path
			FROM asset_checkpoint ac
			JOIN asset a ON ac.asset_id = a.id
			LEFT JOIN collection c ON a.collection_id = c.id
			WHERE ac.group_id = ? AND ac.trashed = 0
		`, gid)
		if err != nil {
			return fmt.Errorf("query group checkpoints: %w", err)
		}
	}

	cpMap := make(map[string]CheckpointRow)
	for _, cp := range checkpoints {
		relDir := strings.Trim(cp.CollPath, "/")
		fileName := cp.AssetName + "." + cp.Extension
		var relPath string
		if relDir == "" {
			relPath = fileName
		} else {
			relPath = filepath.Join(relDir, fileName)
		}
		cpMap[relPath] = cp
	}

	for i := range group.Files {
		f := &group.Files[i]
		cp, ok := cpMap[f.RelPath]
		if !ok {
			continue
		}

		outPath := filepath.Join(commitDir, f.RelPath)
		os.MkdirAll(filepath.Dir(outPath), 0755)

		err = rebuildFileFromChunks(db, cp.Chunks, outPath)
		if err != nil {
			return fmt.Errorf("rebuild %s: %w", f.RelPath, err)
		}
		f.TempPath = outPath
	}
	return nil
}

// StageAll extracts the full timeline and reconstructs all files to disk.
func StageAll(clstPath string, stagingDir string) ([]CommitGroup, error) {
	db, err := sqlx.Open("sqlite3", clstPath+"?_journal=WAL&mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	var rows []CheckpointRow
	err = db.Select(&rows, `
		SELECT 
			ac.id, ac.created_at, ac.asset_id, ac.file_size, ac.chunks, 
			ac.group_id, ac.time_modified,
			a.name AS asset_name, a.extension,
			IFNULL(c.collection_path, '/') AS coll_path
		FROM asset_checkpoint ac
		JOIN asset a ON ac.asset_id = a.id
		LEFT JOIN collection c ON a.collection_id = c.id
		WHERE ac.trashed = 0
		ORDER BY ac.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query checkpoints: %w", err)
	}

	if len(rows) == 0 {
		return nil, fmt.Errorf("no checkpoints found in %s", clstPath)
	}

	var allChunkHashes []string
	err = db.Select(&allChunkHashes, "SELECT hash FROM chunk")
	if err != nil {
		return nil, fmt.Errorf("query chunk hashes: %w", err)
	}
	localChunks := make(map[string]bool, len(allChunkHashes))
	for _, h := range allChunkHashes {
		localChunks[h] = true
	}
	fmt.Printf("  Local chunk store: %d chunks available\n", len(localChunks))

	var availableRows []CheckpointRow
	skipped := 0
	for _, r := range rows {
		hashes := strings.Split(r.Chunks, ",")
		allPresent := true
		for _, h := range hashes {
			if h == "" {
				continue
			}
			if !localChunks[h] {
				allPresent = false
				break
			}
		}
		if allPresent {
			availableRows = append(availableRows, r)
		} else {
			skipped++
		}
	}
	fmt.Printf("  Checkpoints: %d available, %d skipped (missing chunks), %d total\n",
		len(availableRows), skipped, len(rows))

	if len(availableRows) == 0 {
		return nil, fmt.Errorf("no checkpoints with complete chunk data found - sync the project first")
	}

	type groupEntry struct {
		minTime string
		rows    []CheckpointRow
	}
	groupMap := make(map[string]*groupEntry)
	var groupOrder []string
	emptyIdx := 0

	for _, r := range availableRows {
		gid := r.GroupId
		if gid == "" {
			gid = fmt.Sprintf("__solo_%d", emptyIdx)
			emptyIdx++
		}
		if _, ok := groupMap[gid]; !ok {
			groupMap[gid] = &groupEntry{minTime: r.CreatedAt}
			groupOrder = append(groupOrder, gid)
		}
		g := groupMap[gid]
		if r.CreatedAt < g.minTime {
			g.minTime = r.CreatedAt
		}
		g.rows = append(g.rows, r)
	}

	sort.Slice(groupOrder, func(i, j int) bool {
		return groupMap[groupOrder[i]].minTime < groupMap[groupOrder[j]].minTime
	})

	seenAssets := make(map[string]bool)
	var groups []CommitGroup

	for idx, gid := range groupOrder {
		ge := groupMap[gid]
		cg := CommitGroup{
			Index:     idx + 1,
			GroupId:   gid,
			Timestamp: ge.minTime,
		}

		commitDir := filepath.Join(stagingDir, fmt.Sprintf("%04d", idx+1))
		os.MkdirAll(commitDir, 0755)

		for _, r := range ge.rows {
			relDir := strings.Trim(r.CollPath, "/")
			fileName := r.AssetName + "." + r.Extension
			var relPath string
			if relDir == "" {
				relPath = fileName
			} else {
				relPath = filepath.Join(relDir, fileName)
			}

			op := "modify"
			if !seenAssets[r.AssetId] {
				op = "add"
				seenAssets[r.AssetId] = true
			}

			outPath := filepath.Join(commitDir, relPath)
			os.MkdirAll(filepath.Dir(outPath), 0755)

			err = rebuildFileFromChunks(db, r.Chunks, outPath)
			if err != nil {
				return nil, fmt.Errorf("commit %d, rebuild %s: %w", idx+1, relPath, err)
			}

			cg.Files = append(cg.Files, FileOp{
				RelPath:   relPath,
				TempPath:  outPath,
				Operation: op,
				FileSize:  r.FileSize,
			})
		}

		groups = append(groups, cg)
		fmt.Printf("  Staged commit %d/%d (%d files, %.1f MB)\n",
			idx+1, len(groupOrder), len(cg.Files), float64(totalFileSize(cg.Files))/1024/1024)
	}

	timelinePath := filepath.Join(stagingDir, "timeline.json")
	if err := SaveTimeline(groups, timelinePath); err != nil {
		return nil, fmt.Errorf("save timeline: %w", err)
	}

	return groups, nil
}

// rebuildFileFromChunks reassembles a file from its chunk hashes.
func rebuildFileFromChunks(db *sqlx.DB, chunks string, outPath string) error {
	chunkHashes := strings.Split(chunks, ",")

	file, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer file.Close()

	buffer := bytes.Buffer{}
	bufferLimit := 100 * 1024 * 1024 // 100 MiB

	for _, hash := range chunkHashes {
		if hash == "" {
			continue
		}
		var data []byte
		err = db.Get(&data, "SELECT data FROM chunk WHERE hash = ?", hash)
		if err != nil {
			return fmt.Errorf("get chunk %s: %w", hash[:12], err)
		}
		if len(data) == 0 {
			continue
		}
		buffer.Write(data)
		if buffer.Len() > bufferLimit {
			decompressor, err := kzstd.NewReader(&buffer)
			if err != nil {
				return err
			}
			io.Copy(file, decompressor)
			decompressor.Close()
			buffer.Reset()
		}
	}
	if buffer.Len() > 0 {
		decompressor, err := kzstd.NewReader(&buffer)
		if err != nil {
			return err
		}
		io.Copy(file, decompressor)
		decompressor.Close()
		buffer.Reset()
	}
	return nil
}

func totalFileSize(files []FileOp) int64 {
	var total int64
	for _, f := range files {
		total += f.FileSize
	}
	return total
}

// StreamSource holds an open .clst database for streaming extraction.
type StreamSource struct {
	db *sqlx.DB
}

// OpenStream opens a .clst for streaming extraction.
func OpenStream(clstPath string) (*StreamSource, error) {
	db, err := sqlx.Open("sqlite3", clstPath+"?_journal=WAL&mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return &StreamSource{db: db}, nil
}

// Close closes the database.
func (s *StreamSource) Close() error {
	return s.db.Close()
}

// BuildTimeline returns commit groups without reconstructing files.
func (s *StreamSource) BuildTimeline() ([]CommitGroup, error) {
	var rows []CheckpointRow
	err := s.db.Select(&rows, `
		SELECT 
			ac.id, ac.created_at, ac.asset_id, ac.file_size, ac.chunks, 
			ac.group_id, ac.time_modified,
			a.name AS asset_name, a.extension,
			IFNULL(c.collection_path, '/') AS coll_path
		FROM asset_checkpoint ac
		JOIN asset a ON ac.asset_id = a.id
		LEFT JOIN collection c ON a.collection_id = c.id
		WHERE ac.trashed = 0
		ORDER BY ac.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query checkpoints: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no checkpoints found")
	}

	var hashes []string
	err = s.db.Select(&hashes, "SELECT hash FROM chunk")
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	localChunks := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		localChunks[h] = true
	}
	fmt.Printf("  Local chunk store: %d chunks\n", len(localChunks))

	var available []CheckpointRow
	skipped := 0
	for _, r := range rows {
		parts := strings.Split(r.Chunks, ",")
		ok := true
		for _, h := range parts {
			if h != "" && !localChunks[h] {
				ok = false
				break
			}
		}
		if ok {
			available = append(available, r)
		} else {
			skipped++
		}
	}
	fmt.Printf("  Checkpoints: %d available, %d skipped\n", len(available), skipped)

	if len(available) == 0 {
		return nil, fmt.Errorf("no checkpoints with complete chunk data")
	}

	type groupEntry struct {
		minTime string
		rows    []CheckpointRow
	}
	groupMap := make(map[string]*groupEntry)
	var groupOrder []string
	emptyIdx := 0

	for _, r := range available {
		gid := r.GroupId
		if gid == "" {
			gid = fmt.Sprintf("__solo_%d", emptyIdx)
			emptyIdx++
		}
		if _, ok := groupMap[gid]; !ok {
			groupMap[gid] = &groupEntry{minTime: r.CreatedAt}
			groupOrder = append(groupOrder, gid)
		}
		g := groupMap[gid]
		if r.CreatedAt < g.minTime {
			g.minTime = r.CreatedAt
		}
		g.rows = append(g.rows, r)
	}

	sort.Slice(groupOrder, func(i, j int) bool {
		return groupMap[groupOrder[i]].minTime < groupMap[groupOrder[j]].minTime
	})

	seenAssets := make(map[string]bool)
	var groups []CommitGroup

	for idx, gid := range groupOrder {
		ge := groupMap[gid]
		cg := CommitGroup{
			Index:     idx + 1,
			GroupId:   gid,
			Timestamp: ge.minTime,
		}
		for _, r := range ge.rows {
			relDir := strings.Trim(r.CollPath, "/")
			fileName := r.AssetName + "." + r.Extension
			var relPath string
			if relDir == "" {
				relPath = fileName
			} else {
				relPath = filepath.Join(relDir, fileName)
			}
			op := "modify"
			if !seenAssets[r.AssetId] {
				op = "add"
				seenAssets[r.AssetId] = true
			}
			cg.Files = append(cg.Files, FileOp{
				RelPath:   relPath,
				Operation: op,
				FileSize:  r.FileSize,
			})
		}
		groups = append(groups, cg)
	}
	return groups, nil
}

// StageGroup reconstructs files for one commit group.
func (s *StreamSource) StageGroup(group *CommitGroup, stagingDir string) error {
	commitDir := filepath.Join(stagingDir, fmt.Sprintf("%04d", group.Index))
	os.MkdirAll(commitDir, 0755)

	var checkpoints []CheckpointRow
	gid := group.GroupId

	if strings.HasPrefix(gid, "__solo_") {
		err := s.db.Select(&checkpoints, `
			SELECT ac.id, ac.chunks, ac.file_size,
				a.name AS asset_name, a.extension,
				IFNULL(c.collection_path, '/') AS coll_path
			FROM asset_checkpoint ac
			JOIN asset a ON ac.asset_id = a.id
			LEFT JOIN collection c ON a.collection_id = c.id
			WHERE ac.trashed = 0 AND (ac.group_id = '' OR ac.group_id IS NULL)
			ORDER BY ac.created_at ASC
		`)
		if err != nil {
			return fmt.Errorf("query solo checkpoints: %w", err)
		}
	} else {
		err := s.db.Select(&checkpoints, `
			SELECT ac.id, ac.chunks, ac.file_size,
				a.name AS asset_name, a.extension,
				IFNULL(c.collection_path, '/') AS coll_path
			FROM asset_checkpoint ac
			JOIN asset a ON ac.asset_id = a.id
			LEFT JOIN collection c ON a.collection_id = c.id
			WHERE ac.group_id = ? AND ac.trashed = 0
		`, gid)
		if err != nil {
			return fmt.Errorf("query checkpoints: %w", err)
		}
	}

	cpMap := make(map[string]CheckpointRow)
	for _, cp := range checkpoints {
		relDir := strings.Trim(cp.CollPath, "/")
		fileName := cp.AssetName + "." + cp.Extension
		var relPath string
		if relDir == "" {
			relPath = fileName
		} else {
			relPath = filepath.Join(relDir, fileName)
		}
		cpMap[relPath] = cp
	}

	for i := range group.Files {
		f := &group.Files[i]
		cp, ok := cpMap[f.RelPath]
		if !ok {
			continue
		}
		outPath := filepath.Join(commitDir, f.RelPath)
		os.MkdirAll(filepath.Dir(outPath), 0755)
		if err := rebuildFileFromChunks(s.db, cp.Chunks, outPath); err != nil {
			return fmt.Errorf("rebuild %s: %w", f.RelPath, err)
		}
		f.TempPath = outPath
	}
	return nil
}

// CleanGroup removes staged files for a commit group.
func CleanGroup(stagingDir string, index int) error {
	return os.RemoveAll(filepath.Join(stagingDir, fmt.Sprintf("%04d", index)))
}
