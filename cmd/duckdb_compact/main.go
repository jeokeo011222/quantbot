// duckdb_compact 物理压缩 DuckDB 时序数据库。
//
// 背景：DuckDB 的 DELETE 只是逻辑删除，文件物理大小不会缩小（空闲块不回收，
// CHECKPOINT/VACUUM 也无法真正压缩文件）。要让文件真正变小，必须重建数据库文件：
//
//  1. 在源库 DELETE 旧数据（保留最近 N 天）
//  2. EXPORT DATABASE 导出全部表/视图/数据
//  3. 新建空库文件并 IMPORT DATABASE（数据紧凑重写）
//  4. 重建索引、替换原文件
//
// 用法：
//
//	go run ./cmd/duckdb_compact -db build/bin/data/stock.duckdb -keep-days 365
//	go run ./cmd/duckdb_compact -db x.duckdb -keep-date 2025-09-04 -table stock_daily,l1_snapshot
//	go run ./cmd/duckdb_compact -db x.duckdb -keep-days 365 -dry-run   # 仅预览将删除多少行
//	go run ./cmd/duckdb_compact -db x.duckdb -keep-days 365 -force     # 跳过确认
package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

var (
	dbPath   = flag.String("db", "build/bin/data/stock.duckdb", "DuckDB 数据库文件路径")
	tables   = flag.String("table", "stock_daily", "要清理的时序表，逗号分隔多个")
	dateCol  = flag.String("date-col", "date", "日期列名（date 列需要可比较，字符串日期或 DATE 均可）")
	keepDate = flag.String("keep-date", "", "保留该日期及之后的数据 (YYYY-MM-DD)；为空则按 -keep-days 计算")
	keepDays = flag.Int("keep-days", 365, "保留最近 N 天（当 -keep-date 未指定时）")
	dryRun   = flag.Bool("dry-run", false, "仅预览将删除的行数与日期分布，不执行")
	force    = flag.Bool("force", false, "跳过交互确认直接执行")
	keepBak  = flag.Bool("keep-backup", false, "保留原库备份文件（默认压缩完成后删除备份）")
)

type tableStat struct {
	table      string
	total      int64
	keep       int64
	del        int64
	minD, maxD string
}

func main() {
	flag.Parse()

	if *keepDate == "" {
		*keepDate = time.Now().AddDate(0, 0, -*keepDays).Format("2006-01-02")
	}

	tbls := []string{}
	for _, t := range strings.Split(*tables, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tbls = append(tbls, t)
		}
	}
	if len(tbls) == 0 {
		log.Fatal("未指定要清理的表 (-table)")
	}

	abs, err := filepath.Abs(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		log.Fatalf("数据库文件不存在: %s", abs)
	}
	fmt.Printf("数据库: %s\n", abs)
	fmt.Printf("保留日期: %s 之后（含）的数据\n", *keepDate)
	fmt.Printf("清理表: %s\n", strings.Join(tbls, ", "))

	// ---------- 1. 打开源库，预览 ----------
	db, err := sql.Open("duckdb", abs)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.Exec(`SET threads=8;`)
	db.Exec(`SET preserve_insertion_order=false;`)

	var stats []tableStat

	for _, t := range tbls {
		var s tableStat
		s.table = t
		if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t)).Scan(&s.total); err != nil {
			log.Fatalf("表 %s 不存在或不可读: %v", t, err)
		}
		// 确认日期列存在
		var hasCol int
		if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_name=? AND column_name=?`, t, *dateCol).Scan(&hasCol); err != nil || hasCol == 0 {
			log.Fatalf("表 %s 缺少日期列 %q", t, *dateCol)
		}
		qMin := fmt.Sprintf(`SELECT MIN("%s")::VARCHAR, MAX("%s")::VARCHAR FROM "%s"`, *dateCol, *dateCol, t)
		if err := db.QueryRow(qMin).Scan(&s.minD, &s.maxD); err != nil {
			log.Fatalf("读取 %s 日期范围失败: %v", t, err)
		}
		if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE "%s" >= DATE '%s'`, t, *dateCol, *keepDate)).Scan(&s.keep); err != nil {
			log.Fatal(err)
		}
		s.del = s.total - s.keep
		stats = append(stats, s)
	}

	fmt.Println("\n===== 预览 =====")
	for _, s := range stats {
		fmt.Printf("表 %-20s 总行数 %-10d 保留 %-10d 删除 %-10d  范围 %s ~ %s\n",
			s.table, s.total, s.keep, s.del, s.minD, s.maxD)
	}
	delTotal := int64(0)
	keepTotal := int64(0)
	for _, s := range stats {
		delTotal += s.del
		keepTotal += s.keep
	}
	fmt.Printf("合计: 删除 %d 行, 保留 %d 行\n", delTotal, keepTotal)
	sz, _ := os.Stat(abs)
	fmt.Printf("当前文件大小: %.1f MB\n", float64(sz.Size())/1024/1024)

	if *dryRun {
		fmt.Println("\n[dry-run] 预览结束，未做任何修改。")
		return
	}

	// ---------- 2. 交互确认 ----------
	if !*force {
		fmt.Print("\n确认删除以上数据并压缩数据库? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") {
			fmt.Println("已取消。")
			return
		}
	}

	// ---------- 3. 源库 DELETE 旧数据 ----------
	fmt.Println("\n[1/6] 删除旧数据 ...")
	for _, t := range tbls {
		res, err := db.Exec(fmt.Sprintf(`DELETE FROM "%s" WHERE "%s" < DATE '%s'`, t, *dateCol, *keepDate))
		if err != nil {
			log.Fatalf("删除 %s 失败: %v", t, err)
		}
		n, _ := res.RowsAffected()
		fmt.Printf("  表 %s 删除 %d 行\n", t, n)
	}

	// ---------- 4. 收集索引定义（EXPORT/IMPORT 可能已保留索引，重建前需 DROP 保护）----------
	fmt.Println("[2/6] 收集索引定义 ...")
	idxRows, err := db.Query(`SELECT index_name, sql FROM duckdb_indexes() WHERE sql IS NOT NULL`)
	if err != nil {
		log.Printf("  读取索引失败(可忽略): %v", err)
	}
	type idxInfo struct{ name, sql string }
	var idxList []idxInfo
	if idxRows != nil {
		for idxRows.Next() {
			var name, sql string
			if err := idxRows.Scan(&name, &sql); err == nil {
				idxList = append(idxList, idxInfo{name, sql})
			}
		}
		idxRows.Close()
	}
	fmt.Printf("  发现 %d 个索引\n", len(idxList))

	// ---------- 5. EXPORT / IMPORT 重建数据库文件 ----------
	workDir := filepath.Join(filepath.Dir(abs), ".compact_tmp")
	exportDir := filepath.Join(workDir, "export")

	fmt.Println("[3/6] EXPORT DATABASE ...")
	if err := os.RemoveAll(workDir); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		log.Fatal(err)
	}
	expSQL := fmt.Sprintf(`EXPORT DATABASE '%s' (FORMAT CSV)`, strings.ReplaceAll(exportDir, `\`, `/`))
	start := time.Now()
	if _, err := db.Exec(expSQL); err != nil {
		log.Fatalf("EXPORT 失败: %v", err)
	}
	fmt.Printf("  EXPORT 完成 (%v)\n", time.Since(start))
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}

	// 备份原库
	bakPath := abs + ".bak"
	if err := copyFile(abs, bakPath); err != nil {
		log.Fatalf("备份失败: %v", err)
	}
	fmt.Printf("  已备份原库到 %s\n", bakPath)

	// 新建库并 IMPORT
	newPath := filepath.Join(filepath.Dir(abs), "compact_new.duckdb")
	os.Remove(newPath)
	nb, err := sql.Open("duckdb", newPath)
	if err != nil {
		log.Fatal(err)
	}
	nb.Exec(`SET threads=8;`)
	nb.Exec(`SET preserve_insertion_order=false;`)

	fmt.Println("[4/6] IMPORT DATABASE ...")
	impSQL := fmt.Sprintf(`IMPORT DATABASE '%s'`, strings.ReplaceAll(exportDir, `\`, `/`))
	start = time.Now()
	if _, err := nb.Exec(impSQL); err != nil {
		log.Fatalf("IMPORT 失败: %v", err)
	}
	fmt.Printf("  IMPORT 完成 (%v)\n", time.Since(start))

	// 重建索引（EXPORT/IMPORT 可能已保留，先 DROP 再 CREATE 保证幂等）
	fmt.Println("[5/6] 重建索引 ...")
	for _, idx := range idxList {
		if _, err := nb.Exec(fmt.Sprintf(`DROP INDEX IF EXISTS "%s"`, idx.name)); err != nil {
			log.Printf("  清理索引 %s 失败(可忽略): %v", idx.name, err)
		}
		if _, err := nb.Exec(idx.sql); err != nil {
			log.Printf("  重建索引 %s 失败(可忽略): %v", idx.name, err)
		}
	}

	// ---------- 6. 校验新库 ----------
	fmt.Println("[6/6] 校验新库 ...")
	ok := true
	for _, t := range tbls {
		var cnt int64
		if err := nb.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, t)).Scan(&cnt); err != nil {
			log.Printf("  校验失败 %s: %v", t, err)
			ok = false
		} else {
			fmt.Printf("  表 %-20s 剩余 %d 行\n", t, cnt)
			if cnt != keepTotalFor(t, stats) {
				log.Printf("  警告: %s 行数 %d 与预期 %d 不一致", t, cnt, keepTotalFor(t, stats))
			}
		}
	}
	// 校验用户视图可查询（排除 DuckDB 系统目录视图）
	isSystem := func(name string) bool {
		for _, p := range []string{"duckdb_", "pg_", "sqlite_", "pragma_"} {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
	vRows, err := nb.Query(`SELECT view_name FROM duckdb_views() WHERE schema_name='main'`)
	if err == nil {
		for vRows.Next() {
			var v string
			if err := vRows.Scan(&v); err == nil {
				if isSystem(v) {
					continue
				}
				var c int64
				if err := nb.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, v)).Scan(&c); err == nil {
					fmt.Printf("  视图 %-20s %d 行\n", v, c)
				}
			}
		}
		vRows.Close()
	}
	if !ok {
		log.Fatal("新库校验失败，已保留备份，未替换原库。")
	}
	if err := nb.Close(); err != nil {
		log.Fatal(err)
	}

	// 替换原库文件
	if err := os.Remove(abs); err != nil {
		log.Fatalf("删除原库失败: %v", err)
	}
	if err := os.Rename(newPath, abs); err != nil {
		log.Fatalf("重命名失败: %v", err)
	}

	// 清理临时目录与备份
	os.RemoveAll(workDir)
	if !*keepBak {
		os.Remove(bakPath)
		fmt.Println("  已删除备份文件（-keep-backup 可保留）")
	} else {
		fmt.Printf("  备份保留在 %s\n", bakPath)
	}

	nsz, _ := os.Stat(abs)
	fmt.Printf("\n===== 完成 =====\n新文件大小: %.1f MB (原 %.1f MB)\n",
		float64(nsz.Size())/1024/1024, float64(sz.Size())/1024/1024)
	fmt.Println("提示: 发布前请确认 data 目录下无残留 .compact_tmp / *.bak 文件。")
}

func keepTotalFor(t string, stats []tableStat) int64 {
	for _, s := range stats {
		if s.table == t {
			return s.keep
		}
	}
	return -1
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
