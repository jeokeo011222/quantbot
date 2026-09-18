package data

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed tdx_sector_data.json
var embeddedData embed.FS

// StockDictEntry 股票字典条目
type StockDictEntry struct {
	Code string
	Name string
}

// IndustryEntry 行业条目
type IndustryEntry struct {
	Code string
	Name string
}

// StockIndustryMapping 股票行业映射
type StockIndustryMapping struct {
	TDX string `json:"tdx"`
	SW  string `json:"sw"`
}

// StockDictData stock_dict.json 结构
type StockDictData struct {
	Meta struct {
		UpdatedAt string `json:"updated_at"`
		Count     int    `json:"count"`
	} `json:"_meta"`
	ByCode map[string]string `json:"by_code"`
	ByName map[string]string `json:"by_name"`
}

// TDXSectorData tdx_sector_data.json 结构
type TDXSectorData struct {
	Meta struct {
		Source string `json:"source"`
	} `json:"meta"`
	IndustryDefinitions map[string]map[string]string    `json:"industry_definitions"`
	StockIndustry       map[string]StockIndustryMapping `json:"stock_industry"`
}

// DictLoader 数据字典加载器
type DictLoader struct {
	mu            sync.RWMutex
	stockDict     *StockDictData
	sectorData    *TDXSectorData
	codeNameMap   map[string]string // 标准化代码 -> 名称
	nameCodeMap   map[string]string // 名称 -> 标准化代码
	stockList     []StockDictEntry  // 全部股票列表
	industryMap   map[string]string // 行业代码 -> 行业名称
	stockIndustry map[string]string // 股票代码 -> 行业名称
	loaded        bool
	dataDir       string // 外部数据目录（优先加载）
}

var (
	dictLoader     *DictLoader
	dictLoaderOnce sync.Once
)

// GetDictLoader 获取单例加载器
func GetDictLoader() *DictLoader {
	dictLoaderOnce.Do(func() {
		dictLoader = &DictLoader{
			codeNameMap:   make(map[string]string),
			nameCodeMap:   make(map[string]string),
			stockList:     make([]StockDictEntry, 0),
			industryMap:   make(map[string]string),
			stockIndustry: make(map[string]string),
		}
		dictLoader.initDataDir()
		dictLoader.load()
	})
	return dictLoader
}

// initDataDir 初始化外部数据目录
// 优先级：exe目录/data > 工作目录/data
func (dl *DictLoader) initDataDir() {
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		dataDir := filepath.Join(exeDir, "data")
		if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
			dl.dataDir = dataDir
			log.Printf("[DictLoader] 外部数据目录: %s", dataDir)
			return
		}
	}

	// 备选：工作目录下的 data 文件夹
	cwd, err := os.Getwd()
	if err == nil {
		dataDir := filepath.Join(cwd, "data")
		if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
			dl.dataDir = dataDir
			log.Printf("[DictLoader] 外部数据目录(工作目录): %s", dataDir)
			return
		}
	}

	log.Printf("[DictLoader] 未找到外部数据目录，启动时将报错")
}

// load 加载数据：stock_dict.json 必须从外部 data 文件夹加载
// tdx_sector_data.json 优先外部文件 > 嵌入数据
func (dl *DictLoader) load() {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	if dl.loaded {
		return
	}

	// 加载 stock_dict.json：必须从外部 data 文件夹加载，不使用嵌入数据
	if dl.dataDir == "" {
		log.Fatalf("[DictLoader] 外部数据目录未找到，无法加载 stock_dict.json")
	}

	externalPath := filepath.Join(dl.dataDir, "stock_dict.json")
	data, err := os.ReadFile(externalPath)
	if err != nil {
		log.Fatalf("[DictLoader] 无法读取 stock_dict.json: %v (路径: %s)", err, externalPath)
	}

	var dictData StockDictData
	if err := json.Unmarshal(data, &dictData); err != nil {
		log.Fatalf("[DictLoader] stock_dict.json 解析失败: %v", err)
	}

	dl.stockDict = &dictData
	dl.buildStockIndex()
	log.Printf("[DictLoader] stock_dict.json 加载成功: %d 只股票 (来源: %s)", dictData.Meta.Count, externalPath)

	// 加载 tdx_sector_data.json（优先外部文件 > 嵌入数据）
	sectorLoaded := false
	if dl.dataDir != "" {
		externalSectorPath := filepath.Join(dl.dataDir, "tdx_sector_data.json")
		if sectorData, err := os.ReadFile(externalSectorPath); err == nil {
			var tdSectorData TDXSectorData
			if err := json.Unmarshal(sectorData, &tdSectorData); err == nil {
				dl.sectorData = &tdSectorData
				dl.buildSectorIndex()
				log.Printf("[DictLoader] tdx_sector_data.json 从外部加载: %d 条行业映射 (来源: %s)", len(tdSectorData.StockIndustry), externalSectorPath)
				sectorLoaded = true
			} else {
				log.Printf("[DictLoader] tdx_sector_data.json 外部文件解析失败: %v，尝试嵌入数据", err)
			}
		}
	}

	if !sectorLoaded {
		if data, err := fs.ReadFile(embeddedData, "tdx_sector_data.json"); err == nil {
			var tdSectorData TDXSectorData
			if err := json.Unmarshal(data, &tdSectorData); err == nil {
				dl.sectorData = &tdSectorData
				dl.buildSectorIndex()
				log.Printf("[DictLoader] tdx_sector_data.json 从嵌入数据加载: %d 条行业映射", len(tdSectorData.StockIndustry))
				sectorLoaded = true
			} else {
				log.Printf("[DictLoader] tdx_sector_data.json 嵌入数据解析失败: %v", err)
			}
		} else {
			log.Printf("[DictLoader] tdx_sector_data.json 嵌入数据读取失败: %v", err)
		}
	}

	dl.loaded = true
}

// buildStockIndex 构建股票索引
func (dl *DictLoader) buildStockIndex() {
	if dl.stockDict == nil {
		return
	}

	// 构建 code -> name 映射
	// 同时存储多种格式：原始key（sh600777）、带点号key（sh.600777）、标准化key（600777）
	// 避免不同前缀（如sh000001和sz000001）相互覆盖
	for code, name := range dl.stockDict.ByCode {
		lowerCode := strings.ToLower(code)

		// 存储原始key（如 sh600777）
		dl.codeNameMap[lowerCode] = name

		// 存储带点号格式（如 sh.600777）
		if len(lowerCode) >= 8 {
			prefix := lowerCode[:2] // sh/sz/bj
			numPart := lowerCode[2:]
			if isAllDigits(numPart) {
				dl.codeNameMap[prefix+"."+numPart] = name
				dl.codeNameMap[prefix+"_"+numPart] = name
			}
		}

		// 存储标准化key（不带前缀，不带点号）
		normalizedCode := dl.normalizeCode(code)
		if normalizedCode != lowerCode {
			if existing, exists := dl.codeNameMap[normalizedCode]; !exists {
				dl.codeNameMap[normalizedCode] = name
			} else {
				_ = existing
			}
		}

		dl.stockList = append(dl.stockList, StockDictEntry{
			Code: lowerCode,
			Name: name,
		})
	}

	// 构建 name -> code 映射（用于反查）
	for name, code := range dl.stockDict.ByName {
		dl.nameCodeMap[name] = code
	}

	log.Printf("[DictLoader] 股票索引构建完成: %d 只股票, %d 个代码映射", len(dl.stockList), len(dl.codeNameMap))
}

// buildSectorIndex 构建板块索引
func (dl *DictLoader) buildSectorIndex() {
	if dl.sectorData == nil {
		return
	}

	// 构建行业代码 -> 行业名称（处理嵌套结构）
	// industry_definitions 的结构是: { "ZJHHY": { "A": "农、林、牧、渔业", ... }, ... }
	for _, industryGroup := range dl.sectorData.IndustryDefinitions {
		for code, name := range industryGroup {
			dl.industryMap[code] = name
		}
	}

	// 构建股票代码 -> 行业名称映射
	for stockCode, mapping := range dl.sectorData.StockIndustry {
		normalizedCode := dl.normalizeCode(stockCode)
		industryName := dl.resolveIndustryName(mapping)
		if industryName != "" {
			dl.stockIndustry[normalizedCode] = industryName
		}
	}

	log.Printf("[DictLoader] 板块索引构建完成: %d 个行业, %d 条股票-行业映射",
		len(dl.industryMap), len(dl.stockIndustry))
}

// resolveIndustryName 从行业映射解析行业名称
func (dl *DictLoader) resolveIndustryName(mapping StockIndustryMapping) string {
	// 优先使用通达信行业
	if name, ok := dl.industryMap[mapping.TDX]; ok {
		return name
	}
	// 备选申万行业
	if name, ok := dl.industryMap[mapping.SW]; ok {
		return name
	}
	return ""
}

// normalizeCode 标准化股票代码（去除 sh/sz/bj 前缀、点号等分隔符的小写处理）
func (dl *DictLoader) normalizeCode(code string) string {
	lower := strings.ToLower(strings.TrimSpace(code))

	// 去除所有点号、下划线等分隔符
	lower = strings.ReplaceAll(lower, ".", "")
	lower = strings.ReplaceAll(lower, "_", "")
	lower = strings.ReplaceAll(lower, "-", "")

	// 如果已经是纯6位代码，保持不变
	if len(lower) == 6 && isAllDigits(lower) {
		return lower
	}

	// 去除 sh/sz/bj 前缀
	prefixes := []string{"sh", "sz", "bj"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			remaining := lower[len(prefix):]
			// 去除可能的分隔符后检查是否为纯数字
			if len(remaining) > 0 && isAllDigits(remaining) {
				return remaining
			}
			// 如果剩余部分不是纯数字，返回原始去除前缀后的结果
			return remaining
		}
	}

	return lower
}

// isAllDigits 检查是否全为数字
func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ==================== 公共API ====================

// GetStockName 根据代码获取名称
func (dl *DictLoader) GetStockName(code string) string {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	// 统一转换为小写并去除空格
	originalLower := strings.ToLower(strings.TrimSpace(code))

	// 1. 直接查找（原始key，如 sh600777 或 sh.600777）
	if name, ok := dl.codeNameMap[originalLower]; ok {
		return name
	}

	// 2. 去除所有分隔符后查找（处理 sh.600777 -> sh600777, sh_600777 -> sh600777）
	noSeparator := strings.ReplaceAll(originalLower, ".", "")
	noSeparator = strings.ReplaceAll(noSeparator, "_", "")
	noSeparator = strings.ReplaceAll(noSeparator, "-", "")
	if noSeparator != originalLower {
		if name, ok := dl.codeNameMap[noSeparator]; ok {
			return name
		}
	}

	// 3. 标准化后查找（不带前缀，如 600777）
	normalized := dl.normalizeCode(code)
	if normalized != originalLower && normalized != noSeparator {
		if name, ok := dl.codeNameMap[normalized]; ok {
			return name
		}
	}

	// 4. 尝试不同市场前缀查找（如果输入是纯数字代码）
	if len(normalized) == 6 && isAllDigits(normalized) {
		prefixes := []string{"sh", "sz", "bj"}
		for _, prefix := range prefixes {
			// 不带点号
			if name, ok := dl.codeNameMap[prefix+normalized]; ok {
				return name
			}
			// 带点号
			if name, ok := dl.codeNameMap[prefix+"."+normalized]; ok {
				return name
			}
		}
	}

	// 5. 尝试带点号的格式查找
	prefixes := []string{"sh", "sz", "bj"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(originalLower, prefix+".") {
			// 格式: sh.600777 -> 尝试 sh600777
			numPart := originalLower[len(prefix)+1:]
			if isAllDigits(numPart) {
				if name, ok := dl.codeNameMap[prefix+numPart]; ok {
					return name
				}
			}
		}
	}

	// 调试日志：如果找不到名称，记录尝试的格式
	if len(originalLower) > 0 && len(originalLower) < 20 {
		log.Printf("[DictLoader] GetStockName 未找到: code=%s, normalized=%s, 可用映射数=%d",
			code, normalized, len(dl.codeNameMap))
	}

	return "" // 返回空字符串表示未找到
}

// GetStockCode 根据名称获取代码
func (dl *DictLoader) GetStockCode(name string) string {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	if code, ok := dl.nameCodeMap[name]; ok {
		return code
	}
	return ""
}

// GetIndustryByStock 获取股票所属行业
func (dl *DictLoader) GetIndustryByStock(code string) string {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	if industry, ok := dl.stockIndustry[code]; ok {
		return industry
	}
	normalized := dl.normalizeCode(code)
	if industry, ok := dl.stockIndustry[normalized]; ok {
		return industry
	}
	return "通用"
}

// GetIndustryName 根据行业代码获取名称
func (dl *DictLoader) GetIndustryName(industryCode string) string {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	if name, ok := dl.industryMap[industryCode]; ok {
		return name
	}
	return industryCode
}

// GetAllStocks 获取全部股票列表
func (dl *DictLoader) GetAllStocks() []StockDictEntry {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	result := make([]StockDictEntry, len(dl.stockList))
	copy(result, dl.stockList)
	return result
}

// GetStocksByIndustry 根据行业获取股票列表
func (dl *DictLoader) GetStocksByIndustry(industry string) []StockDictEntry {
	dl.mu.RLock()
	defer dl.mu.RUnlock()

	var result []StockDictEntry
	for _, stock := range dl.stockList {
		if dl.stockIndustry[stock.Code] == industry {
			result = append(result, stock)
		}
	}
	return result
}

// GetStockCount 获取股票总数
func (dl *DictLoader) GetStockCount() int {
	dl.mu.RLock()
	defer dl.mu.RUnlock()
	return len(dl.codeNameMap)
}

// GetIndustryCount 获取行业总数
func (dl *DictLoader) GetIndustryCount() int {
	dl.mu.RLock()
	defer dl.mu.RUnlock()
	return len(dl.industryMap)
}

// Reload 重新加载数据
func (dl *DictLoader) Reload() {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	dl.codeNameMap = make(map[string]string)
	dl.nameCodeMap = make(map[string]string)
	dl.stockList = make([]StockDictEntry, 0)
	dl.industryMap = make(map[string]string)
	dl.stockIndustry = make(map[string]string)
	dl.loaded = false

	dl.load()
}
