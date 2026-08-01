// cleanup-vector 清理 VECTOR.gob 和 chunks_index 中的孤立条目。
//
// 用法:
//
//	go run ./cmd/cleanup-vector/                    # 实际执行清理
//	go run ./cmd/cleanup-vector/ --dry-run          # 仅检测，不修改
//	go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics  # 只清理指定 KB
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/BurntSushi/toml"

	"knowledge-mcp/internal/knowledge"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "仅检测，不实际修改")
	kbFilter := flag.String("kb", "", "只清理指定知识库（默认清理全部）")
	flag.Parse()

	// 加载配置
	cfg := loadConfig()

	// 连接 MySQL
	dsn := buildDSN(cfg)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接 MySQL 失败: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "MySQL ping 失败: %v\n", err)
		os.Exit(1)
	}

	// 获取所有 KB
	kbs, err := listKBs(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取 KB 列表失败: %v\n", err)
		os.Exit(1)
	}

	dataDir := expandPath(cfg.DataDir)

	for _, kb := range kbs {
		if *kbFilter != "" && kb != *kbFilter {
			continue
		}

		fmt.Printf("\n━━━ KB: %s ━━━\n", kb)

		// 1. 获取该 KB 下所有有效 chunk（存在于 chunks 表中）
		validChunks, err := loadValidChunks(db, kb)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  加载有效 chunk 失败: %v\n", err)
			continue
		}
		fmt.Printf("  chunks 表有效条目: %d\n", len(validChunks))

		// 2. 清理 VECTOR.gob
		cleanVectorIndex(dataDir, kb, validChunks, *dryRun)

		// 3. 清理 chunks_index 表
		cleanChunksIndex(db, kb, validChunks, *dryRun)
	}

	fmt.Println("\n完成。")
}

// loadConfig 从 knowledge-mcp.toml 加载配置
func loadConfig() config {
	data, err := os.ReadFile("knowledge-mcp.toml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取配置文件失败: %v\n", err)
		os.Exit(1)
	}
	var cfg config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "解析配置失败: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

type config struct {
	DataDir       string `toml:"data_dir"`
	MySQLDSN      string `toml:"mysql_dsn"`
	MySQLUser     string `toml:"mysql_user"`
	MySQLPassword string `toml:"mysql_password"`
	MySQLHost     string `toml:"mysql_host"`
	MySQLPort     string `toml:"mysql_port"`
	MySQLDatabase string `toml:"mysql_database"`
}

func buildDSN(cfg config) string {
	if cfg.MySQLDSN != "" {
		return cfg.MySQLDSN
	}
	port := cfg.MySQLPort
	if port == "" {
		port = "3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true",
		cfg.MySQLUser, cfg.MySQLPassword, cfg.MySQLHost, port, cfg.MySQLDatabase)
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func listKBs(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM knowledge_bases ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kbs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		kbs = append(kbs, name)
	}
	return kbs, rows.Err()
}

// loadValidChunks 返回 KB 中所有 (docSlug, chunkID) 组合，key 为 "slug/chunkID"
func loadValidChunks(db *sql.DB, kbName string) (map[string]bool, error) {
	rows, err := db.Query(
		"SELECT doc_slug, chunk_id FROM chunks WHERE kb_name = ?", kbName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	valid := make(map[string]bool)
	for rows.Next() {
		var slug, chunkID string
		if err := rows.Scan(&slug, &chunkID); err != nil {
			return nil, err
		}
		valid[slug+"/"+chunkID] = true
	}
	return valid, rows.Err()
}

// cleanVectorIndex 清理 VECTOR.gob 中指向不存在 chunk 的条目
func cleanVectorIndex(dataDir, kbName string, validChunks map[string]bool, dryRun bool) {
	path := filepath.Join(dataDir, kbName, "VECTOR.gob")
	idx, err := knowledge.LoadHNSWIndex(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  加载 VECTOR.gob 失败: %v\n", err)
		return
	}
	if idx == nil {
		fmt.Println("  VECTOR.gob: 不存在，跳过")
		return
	}

	totalBefore := idx.Len()
	removed := 0

	// 遍历所有节点 ID（格式: "slug/chunkID"）
	for _, id := range idx.AllIDs() {
		if !validChunks[id] {
			parts := strings.SplitN(id, "/", 2)
			chunkInfo := id
			if len(parts) == 2 {
				chunkInfo = fmt.Sprintf("doc=%s chunk=%s", parts[0], parts[1])
			}
			fmt.Printf("  [vector] 孤立条目: %s\n", chunkInfo)
			if !dryRun {
				idx.Remove(id)
			}
			removed++
		}
	}

	fmt.Printf("  VECTOR.gob: 总条目=%d 孤立=%d", totalBefore, removed)
	if dryRun {
		fmt.Println(" (--dry-run，未实际修改)")
	} else if removed > 0 {
		if err := idx.Save(path); err != nil {
			fmt.Fprintf(os.Stderr, " 保存 VECTOR.gob 失败: %v\n", err)
		} else {
			fmt.Printf(" 已清理，剩余=%d\n", idx.Len())
		}
	} else {
		fmt.Println(" 无需清理")
	}
}

// cleanChunksIndex 清理 chunks_index 表中指向不存在 chunk 的条目
func cleanChunksIndex(db *sql.DB, kbName string, validChunks map[string]bool, dryRun bool) {
	rows, err := db.Query(
		"SELECT doc_slug, index_data FROM chunks_index WHERE kb_name = ?", kbName,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  查询 chunks_index 失败: %v\n", err)
		return
	}
	defer rows.Close()

	type docIndex struct {
		slug string
		data string
	}
	var docs []docIndex
	for rows.Next() {
		var di docIndex
		if err := rows.Scan(&di.slug, &di.data); err != nil {
			fmt.Fprintf(os.Stderr, "  扫描 chunks_index 行失败: %v\n", err)
			return
		}
		docs = append(docs, di)
	}

	totalFixed := 0
	for _, di := range docs {
		var idx knowledge.ChunksIndex
		if err := json.Unmarshal([]byte(di.data), &idx); err != nil {
			fmt.Fprintf(os.Stderr, "  解析 chunks_index JSON 失败 (doc=%s): %v\n", di.slug, err)
			continue
		}

		// 过滤掉孤立条目
		validEntries := idx.Chunks[:0]
		orphanCount := 0
		for _, entry := range idx.Chunks {
			key := di.slug + "/" + entry.ID
			if validChunks[key] {
				validEntries = append(validEntries, entry)
			} else {
				fmt.Printf("  [index] 孤立条目: doc=%s chunk=%s\n", di.slug, entry.ID)
				orphanCount++
			}
		}

		if orphanCount == 0 {
			continue
		}

		idx.Chunks = validEntries
		idx.ChunkCount = len(validEntries)

		if !dryRun {
			newData, err := json.Marshal(idx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  序列化 chunks_index 失败 (doc=%s): %v\n", di.slug, err)
				continue
			}
			_, err = db.Exec(
				"UPDATE chunks_index SET index_data = ? WHERE kb_name = ? AND doc_slug = ?",
				string(newData), kbName, di.slug,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  更新 chunks_index 失败 (doc=%s): %v\n", di.slug, err)
				continue
			}
		}
		totalFixed++
		fmt.Printf("  [index] doc=%s: 移除 %d 条孤立索引，剩余 %d 条\n", di.slug, orphanCount, len(validEntries))
	}

	if totalFixed == 0 {
		fmt.Println("  chunks_index: 无需清理")
	} else if dryRun {
		fmt.Printf("  chunks_index: 共 %d 个文档需要修复 (--dry-run，未实际修改)\n", totalFixed)
	} else {
		fmt.Printf("  chunks_index: 已修复 %d 个文档\n", totalFixed)
	}
}
