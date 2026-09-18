// Package llmstore 提供基于文件的 LLM 调用日志存储。
// 日志体积较大，因此不写入数据库，而是以 JSON Lines 格式按天追加到
// 可执行文件目录下的 log 文件夹（llm_calls_YYYY-MM-DD.jsonl）。
package llmstore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry 一条 LLM 调用记录（json tag 采用小驼峰，与前端一致）
type Entry struct {
	ID               int64     `json:"id"`
	TaskDate         string    `json:"taskDate"`
	AgentRole        string    `json:"agentRole"`
	TaskName         string    `json:"taskName"`
	Phase            string    `json:"phase"`
	Model            string    `json:"model"`
	InputMessages    string    `json:"inputMessages"`
	OutputContent    string    `json:"outputContent"`
	FinishReason     string    `json:"finishReason"`
	ToolCallCount    int       `json:"toolCallCount"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	TotalTokens      int       `json:"totalTokens"`
	DurationMs       int64     `json:"durationMs"`
	Status           string    `json:"status"`
	ErrorMessage     string    `json:"errorMessage"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Store 基于文件的 LLM 调用日志存储
type Store struct {
	dir string
}

// NewStore 创建文件存储，目录默认为可执行文件目录下的 log 文件夹
func NewStore() *Store {
	return &Store{dir: defaultLogDir()}
}

func defaultLogDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "log"
	}
	return filepath.Join(filepath.Dir(exePath), "log")
}

// FileForDate 返回某天的日志文件路径
func (s *Store) FileForDate(date string) string {
	return filepath.Join(s.dir, "llm_calls_"+date+".jsonl")
}

// Save 追加一条记录到对应日期的日志文件（异步调用，失败仅记录日志）
func (s *Store) Save(rec Entry) error {
	if rec.TaskDate == "" {
		rec.TaskDate = time.Now().Format("2006-01-02")
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.FileForDate(rec.TaskDate), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// query 读取日志并以 createdAt 倒序返回
func (s *Store) query(date, role, status string, limit int) ([]Entry, error) {
	var files []string
	if date != "" {
		p := s.FileForDate(date)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			files = append(files, p)
		}
	} else {
		matches, err := filepath.Glob(filepath.Join(s.dir, "llm_calls_*.jsonl"))
		if err != nil {
			return nil, err
		}
		files = matches
	}
	sort.Strings(files)

	var all []Entry
	for _, f := range files {
		entries, err := readFile(f)
		if err != nil {
			continue
		}
		all = append(all, entries...)
	}

	// 过滤
	var filtered []Entry
	for _, e := range all {
		if role != "" && e.AgentRole != role {
			continue
		}
		if status != "" && e.Status != status {
			continue
		}
		filtered = append(filtered, e)
	}

	// 按时间倒序
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	if limit <= 0 {
		limit = 100
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// Query 查询调用日志
func (s *Store) Query(date, role, status string, limit int) ([]Entry, error) {
	return s.query(date, role, status, limit)
}

// Stats 统计某日（date 为空则统计全部文件）调用次数与 token 消耗
func (s *Store) Stats(date string) (callCount int64, totalTokens int64, err error) {
	if date != "" {
		entries, err := readFile(s.FileForDate(date))
		if err != nil {
			return 0, 0, nil
		}
		for _, e := range entries {
			callCount++
			totalTokens += int64(e.TotalTokens)
		}
		return callCount, totalTokens, nil
	}

	matches, err := filepath.Glob(filepath.Join(s.dir, "llm_calls_*.jsonl"))
	if err != nil {
		return 0, 0, err
	}
	for _, f := range matches {
		entries, err := readFile(f)
		if err != nil {
			continue
		}
		for _, e := range entries {
			callCount++
			totalTokens += int64(e.TotalTokens)
		}
	}
	return callCount, totalTokens, nil
}

// readFile 读取单个日志文件，按行解析，ID 为行号（从 1 开始）
func readFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	lineNo := int64(0)
	for scanner.Scan() {
		lineNo++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			fmt.Fprintf(os.Stderr, "[llmstore] 解析失败行 %d: %v\n", lineNo, err)
			continue
		}
		e.ID = lineNo
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}