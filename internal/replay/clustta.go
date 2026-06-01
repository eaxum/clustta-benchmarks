package replay

import (
	"clustta-benchmarks/internal/extract"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/jotfs/fastcdc-go"
	kzstd "github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

// ClusttaReplayer benchmarks Clustta's chunking pipeline.
type ClusttaReplayer struct {
	workDir    string
	clstPath   string
	db         *sqlx.DB
	seenChunks map[string]bool
	assetIds   map[string]string // relPath -> asset ID
}

func NewClusttaReplayer() *ClusttaReplayer {
	return &ClusttaReplayer{
		seenChunks: make(map[string]bool),
		assetIds:   make(map[string]string),
	}
}

func (c *ClusttaReplayer) Name() string {
	return "Clustta"
}

func (c *ClusttaReplayer) Init(workDir string) error {
	c.workDir = workDir
	os.MkdirAll(workDir, 0755)

	c.clstPath = filepath.Join(workDir, "benchmark.clst")

	db, err := sqlx.Open("sqlite3", c.clstPath+"?_journal=WAL")
	if err != nil {
		return fmt.Errorf("create db: %w", err)
	}
	c.db = db

	schema := `
		CREATE TABLE IF NOT EXISTS chunk (
			hash TEXT PRIMARY KEY NOT NULL,
			data BLOB NOT NULL,
			size INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS asset (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			extension TEXT NOT NULL,
			collection_path TEXT DEFAULT '' NOT NULL
		);
		CREATE TABLE IF NOT EXISTS asset_checkpoint (
			id TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			asset_id TEXT NOT NULL,
			xxhash_checksum TEXT NOT NULL,
			time_modified INTEGER NOT NULL,
			file_size INTEGER NOT NULL,
			chunks TEXT NOT NULL,
			comment TEXT DEFAULT '' NOT NULL,
			group_id TEXT DEFAULT '' NOT NULL,
			FOREIGN KEY (asset_id) REFERENCES asset(id)
		);
	`
	_, err = db.Exec(schema)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	return nil
}

func (c *ClusttaReplayer) ReplayCommit(group extract.CommitGroup) (CommitMetrics, error) {
	var modifiedSize int64
	groupId := uuid.New().String()

	for _, f := range group.Files {
		destPath := filepath.Join(c.workDir, f.RelPath)
		os.MkdirAll(filepath.Dir(destPath), 0755)
		if err := copyFile(f.TempPath, destPath); err != nil {
			return CommitMetrics{}, fmt.Errorf("copy %s: %w", f.RelPath, err)
		}
	}

	start := time.Now()

	for _, f := range group.Files {
		filePath := filepath.Join(c.workDir, f.RelPath)
		modifiedSize += f.FileSize

		tx, err := c.db.Beginx()
		if err != nil {
			return CommitMetrics{}, err
		}

		assetId, ok := c.assetIds[f.RelPath]
		if !ok {
			assetId = uuid.New().String()
			ext := ""
			name := f.RelPath
			if dotIdx := strings.LastIndex(filepath.Base(f.RelPath), "."); dotIdx > 0 {
				base := filepath.Base(f.RelPath)
				name = base[:dotIdx]
				ext = base[dotIdx+1:]
			}
			collPath := filepath.Dir(f.RelPath)
			if collPath == "." {
				collPath = ""
			}
			_, err = tx.Exec("INSERT INTO asset (id, name, extension, collection_path) VALUES (?, ?, ?, ?)",
				assetId, name, ext, collPath)
			if err != nil {
				tx.Rollback()
				return CommitMetrics{}, fmt.Errorf("insert asset: %w", err)
			}
			c.assetIds[f.RelPath] = assetId
		}

		chunkSeq, err := c.storeFileChunks(tx, filePath)
		if err != nil {
			tx.Rollback()
			return CommitMetrics{}, fmt.Errorf("chunk %s: %w", f.RelPath, err)
		}

		fi, err := os.Stat(filePath)
		if err != nil {
			tx.Rollback()
			return CommitMetrics{}, err
		}

		cpId := uuid.New().String()
		_, err = tx.Exec(`INSERT INTO asset_checkpoint 
			(id, created_at, asset_id, xxhash_checksum, time_modified, file_size, chunks, comment, group_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cpId, time.Now().Unix(), assetId, "", fi.ModTime().Unix(), fi.Size(), chunkSeq, fmt.Sprintf("commit %d", group.Index), groupId)
		if err != nil {
			tx.Rollback()
			return CommitMetrics{}, fmt.Errorf("insert checkpoint: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return CommitMetrics{}, err
		}
	}

	checkpointTime := time.Since(start).Seconds()

	clstInfo, err := os.Stat(c.clstPath)
	clstSize := int64(0)
	if err == nil {
		clstSize = clstInfo.Size() / (1024 * 1024)
	}
	totalSize := dirSizeMB(c.workDir)

	return CommitMetrics{
		CommitNr:       group.Index,
		LocalSizeMB:    totalSize,
		MetadataSizeMB: clstSize,
		ServerSizeMB:   clstSize,
		ModifiedFileMB: modifiedSize / (1024 * 1024),
		CommitTimeSec:  checkpointTime,
	}, nil
}

func (c *ClusttaReplayer) Cleanup() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// storeFileChunks runs FastCDC + SHA-256 + zstd + dedup INSERT.
func (c *ClusttaReplayer) storeFileChunks(tx *sqlx.Tx, filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	kiB := 1024
	miB := 1024 * kiB

	opts := fastcdc.Options{
		MinSize:     512 * kiB,
		AverageSize: 1 * miB,
		MaxSize:     8 * miB,
	}

	chunker, err := fastcdc.NewChunker(file, opts)
	if err != nil {
		return "", fmt.Errorf("create chunker: %w", err)
	}

	var chunkSequence []string

	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		data := chunk.Data
		sha256Hash := sha256.New()
		sha256Hash.Write(data)
		hash := hex.EncodeToString(sha256Hash.Sum(nil))

		if c.chunkExists(hash, tx) {
			chunkSequence = append(chunkSequence, hash)
			continue
		}

		encoder, err := kzstd.NewWriter(nil, kzstd.WithEncoderLevel(kzstd.SpeedDefault))
		if err != nil {
			return "", err
		}
		compressedData := encoder.EncodeAll(data, nil)
		encoder.Close()

		size := len(compressedData)
		_, err = tx.Exec("INSERT INTO chunk (hash, data, size) VALUES (?, ?, ?)",
			hash, compressedData, size)
		if err != nil {
			return "", err
		}
		c.seenChunks[hash] = true
		chunkSequence = append(chunkSequence, hash)
	}

	return strings.Join(chunkSequence, ","), nil
}

func (c *ClusttaReplayer) chunkExists(hash string, tx *sqlx.Tx) bool {
	if c.seenChunks[hash] {
		return true
	}
	var h string
	tx.Get(&h, "SELECT hash FROM chunk WHERE hash = ?", hash)
	if h != "" {
		c.seenChunks[hash] = true
		return true
	}
	return false
}
