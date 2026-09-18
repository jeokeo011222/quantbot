package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/version"
)

// ConfigManager 配置管理器
type ConfigManager struct {
	mu     sync.RWMutex
	config *AppConfig
	path   string
}

// SourceToggle 单一数据源开关与优先级配置（优先级数值越小越先尝试）
type SourceToggle struct {
	Enabled  bool `json:"enabled"`
	Priority int  `json:"priority"`
}

// SixDimSourceConfig 市场六维判势数据源配置（设置-数据源页面配置，持久化到 config.json）
// 优先级越小越优先；高优先级源失败自动回退低优先级源（DuckDB 兜底代理），全程无伪造。
type SixDimSourceConfig struct {
	Northbound      SourceToggle `json:"northbound"`        // 同花顺 hsgtApi 北向实时净流入
	Margin          SourceToggle `json:"margin"`            // 东财数据中心融资余额连续增减天数
	VolumeExpansion SourceToggle `json:"volume_expansion"`  // 全市场成交额连续放量天数（DuckDB 兜底）
	THSBoards       SourceToggle `json:"ths_boards"`        // 同花顺官方涨停/跌停/炸板池 + 连板天梯（真实炸板率）
	LimitBoard      SourceToggle `json:"limit_board"`       // 东财 push2ex 实时涨停/跌停/炸板池
	FullMarketStats SourceToggle `json:"full_market_stats"` // DuckDB 全市场收盘统计（兜底）
	Overnight       SourceToggle `json:"overnight"`         // 腾讯全球指数真实隔夜外围
}

// THSConfig 同花顺官方金融数据服务配置（fuyao.aicubes.cn，付费 API Key）。
// 用于：财务数据同步源切换、六维判势情绪/特色数据源、估值/竞价/复权因子等。
type THSConfig struct {
	Enabled bool   `json:"enabled"`  // 是否启用同花顺官方数据源
	APIKey  string `json:"api_key"`  // 官方 API Key（X-api-key，明文存储，与其他 Key 一致）
	BaseURL string `json:"base_url"` // 服务地址，空=官方默认 https://fuyao.aicubes.cn
}

// AppConfig 应用配置结构
type AppConfig struct {
	// 基础设置
	Market      string `json:"market"`
	TradingMode string `json:"trading_mode"` // paper, live

	// AI 设置
	AIProvider string `json:"ai_provider"`
	AIModel    string `json:"ai_model"`
	AIBaseURL  string `json:"ai_base_url"`
	AIAPIKey   string `json:"ai_api_key"` // 明文存储

	// RiskOS 设置
	RiskOSEnabled bool   `json:"riskos_enabled"`
	RiskOSAPIKey  string `json:"riskos_api_key"` // 明文存储

	// Broker / 交易接口
	BrokerType    string `json:"broker_type"`
	BrokerAPIKey  string `json:"broker_api_key"` // 明文存储
	BrokerBaseURL string `json:"broker_base_url"`
	BrokerAccount string `json:"broker_account"`

	// QMT (迅投 XtQuant) 实盘交易接口
	QMTEnabled      bool   `json:"qmt_enabled"`       // 是否启用 QMT 实盘交易
	QMTPath         string `json:"qmt_path"`          // XtQuant 文件夹路径
	QMTAccount      string `json:"qmt_account"`       // 资金账号
	QMTAccountType  string `json:"qmt_account_type"`  // 账号类型: STOCK / CREDIT
	QMTMiniQMT      bool   `json:"qmt_mini_qmt"`      // MiniQMT 模式
	QMTStrategyName string `json:"qmt_strategy_name"` // 策略名
	QMTStrategyPath string `json:"qmt_strategy_path"` // 策略路径
	// 盘中自动买卖实盘开关：QMT 实盘模式下，是否把盘中自动买入/自动卖出/止盈止损也真实下发券商。
	// 独立于手动/确认下单：默认关闭，避免自动执行在未充分验证时触碰真实资金。
	QMTApplyAutoExecution bool `json:"qmt_auto_execution"`

	// 数据源接口
	DataProvider string `json:"data_provider"` // native_tdx, tdx_mcp, mcp, tdx_terminal
	TDXPath      string `json:"tdx_path"`      // 通达信安装路径
	MCPURL       string `json:"mcp_url"`       // MCP 服务地址
	MCPAPIKey    string `json:"mcp_api_key"`   // MCP API Key

	// 市场六维判势数据源配置（设置-数据源页面配置，优先级越小越优先；高优先级源失败自动回退）
	SixDimSource SixDimSourceConfig `json:"sixdim_source"`

	// 同花顺官方金融数据服务（fuyao.aicubes.cn，付费 API Key；可用于财务同步/情绪源/估值/复权等）
	THS THSConfig `json:"ths_source"`

	// 交易参数
	InitialCapital  float64 `json:"initial_capital"`
	Currency        string  `json:"currency"`
	MaxPositionSize float64 `json:"max_position_size"`
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`

	// 运行时
	AutoDailyRun bool   `json:"auto_daily_run"`
	DailyRunTime string `json:"daily_run_time"`

	// 调度器幂等状态（持久化，程序重启后可恢复"当日某任务已执行"，避免启动重跑）
	LastDailyReviewDate string `json:"last_daily_review_date"` // 最近一次每日复盘生成的日期 (2006-01-02)
	LastPlanRefreshDate string `json:"last_plan_refresh_date"` // 最近一次每日投资方案自动重建的日期 (2006-01-02)

	// 实时活动刷新间隔（分钟，1-60，默认5）
	ActivityRefreshMinutes int `json:"activity_refresh_minutes"`

	// 是否记录大模型调用输入/输出内容（审计监控，默认关闭）
	EnableLLMLogging bool `json:"enable_llm_logging"`

	// 是否生成 log/ 日志文件（默认开启；该参数仅允许用户手动修改 config.json，前端程序不提供修改入口）
	EnableLogFile bool `json:"enable_log_file"`

	// 版本管理
	PricingTier string `json:"pricing_tier"` // free, pro, enterprise

	// 自动升级
	AutoUpdate      bool   `json:"auto_update"`       // 启动后自动检查 GitHub Releases（固定周期，无需配置）
	UpdateRepoOwner string `json:"update_repo_owner"` // GitHub 仓库所有者
	UpdateRepoName  string `json:"update_repo_name"`  // GitHub 仓库名

	// 版本
	Version string `json:"version"`

	// 更新时间
	UpdatedAt time.Time `json:"updated_at"`
}

// NewConfigManager 创建配置管理器
// 仅使用可执行文件目录下的config文件夹
func NewConfigManager() (*ConfigManager, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("无法获取可执行文件路径: %w", err)
	}
	exeDir := filepath.Dir(exePath)
	configDir := filepath.Join(exeDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建配置目录: %w", err)
	}

	path := filepath.Join(configDir, "config.json")

	cm := &ConfigManager{
		path: path,
	}

	if err := cm.load(); err != nil {
		// 配置不存在或损坏，使用默认值
		cm.config = cm.defaultConfig()
		if err := cm.save(); err != nil {
			return nil, err
		}
	}

	return cm, nil
}

// load 加载配置
func (cm *ConfigManager) load() error {
	data, err := os.ReadFile(cm.path)
	if err != nil {
		return err
	}

	var config AppConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}

	// 向后兼容：迁移旧格式
	needMigrate := false

	// 1. 处理加密字段（旧版 ai_api_key_enc）
	if config.AIAPIKey == "" && len(data) > 0 {
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err == nil {
			if encKey, ok := raw["ai_api_key_enc"].(string); ok && encKey != "" {
				config.AIAPIKey = encKey
				log.Printf("[Config] Migrated AI API key from encrypted field")
				needMigrate = true
			}
		}
	}

	// 2. 处理 enc: 前缀（旧版简单加密）
	if strings.HasPrefix(config.AIAPIKey, "enc:") {
		config.AIAPIKey = config.AIAPIKey[4:]
		log.Printf("[Config] Migrated AI API key from enc: prefix")
		needMigrate = true
	}

	// 3. 迁移旧版 MCP URL
	if config.MCPURL == "http://127.0.0.1:8000" || config.MCPURL == "" {
		config.MCPURL = "http://127.0.0.1:8765"
		log.Printf("[Config] Migrated MCP URL to %s", config.MCPURL)
		needMigrate = true
	}

	// 4. 实时活动刷新间隔默认值（旧配置无此字段时为0）
	if config.ActivityRefreshMinutes <= 0 || config.ActivityRefreshMinutes > 60 {
		config.ActivityRefreshMinutes = 5
		needMigrate = true
	}

	// 5. 自动升级默认值（旧配置无此字段）
	if config.UpdateRepoOwner == "" {
		config.UpdateRepoOwner = "jeokeo011222"
		needMigrate = true
	}
	if config.UpdateRepoName == "" {
		config.UpdateRepoName = "quantbot"
		needMigrate = true
	}

	// 6. 日志文件开关默认值：旧配置无 enable_log_file 字段时默认启用（仅用户手动改 config.json 关闭）
	if needLogFileDefault(data, &config) {
		needMigrate = true
	}

	// 7. 六维判势数据源配置默认值（旧配置无 sixdim_source 字段时，全部优先级为0）
	if isSixDimSourceZero(config.SixDimSource) {
		config.SixDimSource = defaultSixDimSource()
		needMigrate = true
	} else if needSixDimTHSDefault(data) {
		// 中间版本配置：sixdim_source 已存在但缺少 ths_boards 字段，补默认（默认开启，未配置 Key 自动回退东财）
		config.SixDimSource.THSBoards = SourceToggle{Enabled: true, Priority: 1}
		log.Printf("[Config] Migrated sixdim_source: filled ths_boards default")
		needMigrate = true
	}

	// 8. 同花顺官方数据源默认值（旧配置无 ths_source 字段时默认关闭，不引入额外网络依赖）
	if !needTHSDefault(data, &config) {
		config.THS = THSConfig{Enabled: false}
		needMigrate = true
	}

	cm.config = &config

	// 配置版本迁移
	if config.Version == "" || config.Version < "1.1.0" {
		log.Printf("[Config] Old version detected (version=%s), running migration...", config.Version)
		if config.DataProvider == "" || config.DataProvider == "tdx_local" {
			config.DataProvider = "native_tdx"
			log.Printf("[Config] Migrated data_provider from 'tdx_local' to 'native_tdx'")
			needMigrate = true
		}
		if config.TradingMode == "paper" {
			config.TradingMode = "simulated"
			log.Printf("[Config] Migrated trading_mode from 'paper' to 'simulated'")
			needMigrate = true
		}
		config.Version = "1.1.0"
		needMigrate = true
	}

	if needMigrate {
		log.Printf("[Config] Auto-migration applied, saving updated config")
		return cm.save()
	}

	return nil
}

// save 保存配置。并发安全由调用方保证（运行时写路径已持 cm.mu；仍有部分启动期路径不带锁调用，故此处不重复加锁）。
// 采用「临时文件 + rename」原子替换：同目录临时文件写完后再 os.Rename 覆盖正式文件，
// 避免写盘中途崩溃留下半个/损坏的 config.json；失败自动清理临时文件。
func (cm *ConfigManager) save() error {
	cm.config.UpdatedAt = time.Now()
	cm.config.Version = version.Version

	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return err
	}

	tmp := cm.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, cm.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
}

// defaultConfig 返回默认配置
func (cm *ConfigManager) defaultConfig() *AppConfig {
	return &AppConfig{
		Market:                 "CN",
		TradingMode:            "simulated",
		AIProvider:             "deepseek",
		AIModel:                "deepseek-v4-flash",
		AIBaseURL:              "https://api.deepseek.com",
		RiskOSEnabled:          false,
		BrokerType:             "paper",
		BrokerBaseURL:          "",
		BrokerAccount:          "",
		QMTEnabled:             false,
		QMTPath:                "",
		QMTAccount:             "",
		QMTAccountType:         "STOCK",
		QMTMiniQMT:             true,
		QMTStrategyName:        "QuantBot",
		QMTStrategyPath:        "",
		QMTApplyAutoExecution:  false,
		DataProvider:           "native_tdx",
		TDXPath:                "D:\\tdx",
		MCPURL:                 "http://127.0.0.1:8765", // LLM MCP 服务地址
		MCPAPIKey:              "",
		SixDimSource:           defaultSixDimSource(),
		THS:                    THSConfig{Enabled: false, APIKey: "", BaseURL: ""},
		InitialCapital:         100000.0,
		Currency:               "CNY",
		MaxPositionSize:        0.30,
		MaxDrawdownPct:         0.20,
		AutoDailyRun:           true,
		DailyRunTime:           "15:00",
		ActivityRefreshMinutes: 5,
		EnableLogFile:          true, // 默认生成日志文件（用户可在 config.json 手动改 enable_log_file 关闭）
		PricingTier:            "unified",
		AutoUpdate:             true,
		UpdateRepoOwner:        "jeokeo011222",
		UpdateRepoName:         "quantbot",
		Version:                version.Version,
		UpdatedAt:              time.Now(),
	}
}

// ==================== Getters and Setters ====================

// GetConfig 获取当前配置（只读副本）
func (cm *ConfigManager) GetConfig() AppConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return *cm.config
}

// GetConfigPath 获取配置文件路径
func (cm *ConfigManager) GetConfigPath() string {
	return cm.path
}

// UpdateConfig 更新配置
func (cm *ConfigManager) UpdateConfig(updateFn func(cfg *AppConfig)) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cfg := *cm.config
	updateFn(&cfg)

	cm.config = &cfg
	return cm.save()
}

// SetAPIKey 设置 AI API Key
func (cm *ConfigManager) SetAPIKey(provider, apiKey string) error {
	apiKey = normalizeAPIKey(apiKey)
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.AIProvider = provider
		cfg.AIAPIKey = apiKey
	})
}

// SetAISettings 设置 AI 完整配置
func (cm *ConfigManager) SetAISettings(provider, model, baseURL, apiKey string) error {
	apiKey = normalizeAPIKey(apiKey)
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.AIProvider = provider
		cfg.AIModel = model
		cfg.AIBaseURL = baseURL
		cfg.AIAPIKey = apiKey
	})
}

// SetRiskOSAPIKey 设置 RiskOS API Key
func (cm *ConfigManager) SetRiskOSAPIKey(apiKey string, enabled bool) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.RiskOSAPIKey = apiKey
		cfg.RiskOSEnabled = enabled
	})
}

// SetBrokerAPIKey 设置 Broker API Key
func (cm *ConfigManager) SetBrokerAPIKey(apiKey string, brokerType string) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.BrokerAPIKey = apiKey
		cfg.BrokerType = brokerType
	})
}

// SetMarket 设置市场
func (cm *ConfigManager) SetMarket(market string) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.Market = market
	})
}

// SetTradingMode 设置交易模式
func (cm *ConfigManager) SetTradingMode(mode string) error {
	if mode != "simulated" && mode != "live" && mode != "paper" {
		return fmt.Errorf("invalid trading mode: %s (must be 'simulated' or 'live')", mode)
	}
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.TradingMode = mode
	})
}

// SetInitialCapital 设置初始资金（允许任意非负值，0 表示无固定期初资金）
func (cm *ConfigManager) SetInitialCapital(capital float64) error {
	if capital < 0 {
		return fmt.Errorf("initial capital must be non-negative")
	}
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.InitialCapital = capital
	})
}

// SetActivityRefreshMinutes 设置实时活动刷新间隔（分钟，1-60整数）
func (cm *ConfigManager) SetActivityRefreshMinutes(minutes int) error {
	if minutes < 1 || minutes > 60 {
		return fmt.Errorf("实时活动刷新间隔必须在1-60分钟之间（整数），当前: %d", minutes)
	}
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.ActivityRefreshMinutes = minutes
	})
}

// SetEnableLLMLogging 设置是否记录大模型调用输入/输出内容
func (cm *ConfigManager) SetEnableLLMLogging(enabled bool) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.EnableLLMLogging = enabled
	})
}

// SetDataProvider 设置数据源提供商
func (cm *ConfigManager) SetDataProvider(provider string) error {
	validProviders := map[string]bool{
		"native_tdx":   true,
		"tdx_mcp":      true,
		"mcp":          true,
		"tdx_terminal": true,
		"tencent":      true,
	}
	if !validProviders[provider] {
		return fmt.Errorf("invalid data provider: %s (valid: native_tdx, tdx_mcp, mcp, tdx_terminal, tencent)", provider)
	}
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.DataProvider = provider
	})
}

// SetTDXPath 设置通达信安装路径
func (cm *ConfigManager) SetTDXPath(path string) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.TDXPath = path
	})
}

// SetMCPConfig 设置 MCP 服务配置
func (cm *ConfigManager) SetMCPConfig(url, apiKey string) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.MCPURL = url
		cfg.MCPAPIKey = apiKey
	})
}

// SetBrokerConfig 设置交易接口配置
func (cm *ConfigManager) SetBrokerConfig(brokerType, baseURL, account, apiKey string) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.BrokerType = brokerType
		cfg.BrokerBaseURL = baseURL
		cfg.BrokerAccount = account
		cfg.BrokerAPIKey = apiKey
	})
}

// SetQMTApplyAutoExecution 设置盘中自动买卖实盘开关（独立于手动/确认下单）。
func (cm *ConfigManager) SetQMTApplyAutoExecution(enabled bool) error {
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.QMTApplyAutoExecution = enabled
	})
}

// SetQMTConfig 设置 QMT (迅投 XtQuant) 实盘交易接口配置
func (cm *ConfigManager) SetQMTConfig(enabled bool, path, account, accountType string, miniQMT bool, strategyName, strategyPath string) error {
	if accountType != "STOCK" && accountType != "CREDIT" {
		return fmt.Errorf("invalid QMT account type: %s (must be 'STOCK' or 'CREDIT')", accountType)
	}
	return cm.UpdateConfig(func(cfg *AppConfig) {
		cfg.QMTEnabled = enabled
		cfg.QMTPath = path
		cfg.QMTAccount = account
		cfg.QMTAccountType = accountType
		cfg.QMTMiniQMT = miniQMT
		cfg.QMTStrategyName = strategyName
		cfg.QMTStrategyPath = strategyPath
	})
}

// ResetDefaults 重置为默认配置
func (cm *ConfigManager) ResetDefaults() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.config = cm.defaultConfig()
	return cm.save()
}

// ==================== 辅助函数 ====================

// normalizeAPIKey 标准化API key格式
func normalizeAPIKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimPrefix(key, "Bearer ")
	key = strings.TrimPrefix(key, "BEARER ")
	key = strings.TrimSpace(key)
	return key
}

// needLogFileDefault 当 config.json 中不存在 enable_log_file 字段时，取其默认值 true（启用）。
// 返回是否发生了默认值填充（需要触发保存）。
func needLogFileDefault(data []byte, config *AppConfig) bool {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	if _, ok := raw["enable_log_file"]; ok {
		return false
	}
	config.EnableLogFile = true
	return true
}

// needSixDimTHSDefault 判断 config.json 的 sixdim_source 节是否已包含 ths_boards 字段。
// 返回 true 表示字段已存在（无需填充）。旧配置无该字段时返回 false，由调用方按默认开启。
func needSixDimTHSDefault(data []byte) bool {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	sixdimRaw, ok := raw["sixdim_source"].(map[string]interface{})
	if !ok {
		return false
	}
	_, ok = sixdimRaw["ths_boards"]
	return ok
}

// needTHSDefault 当 config.json 中不存在 ths_source 字段时，取其默认值（关闭，不引入额外网络依赖）。
// 返回 true 表示字段已存在（无需填充）。
func needTHSDefault(data []byte, config *AppConfig) bool {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	if _, ok := raw["ths_source"]; ok {
		return true
	}
	config.THS = THSConfig{Enabled: false}
	return false
}

// defaultSixDimSource 六维判势数据源默认配置（与判势引擎既有取数链一致）
func defaultSixDimSource() SixDimSourceConfig {
	return SixDimSourceConfig{
		Northbound:      SourceToggle{Enabled: true, Priority: 1},
		Margin:          SourceToggle{Enabled: true, Priority: 2},
		VolumeExpansion: SourceToggle{Enabled: true, Priority: 3},
		THSBoards:       SourceToggle{Enabled: true, Priority: 1}, // 同花顺官方情绪源（未配置 Key 时自动回退东财）
		LimitBoard:      SourceToggle{Enabled: true, Priority: 1},
		FullMarketStats: SourceToggle{Enabled: true, Priority: 2},
		Overnight:       SourceToggle{Enabled: true, Priority: 1},
	}
}

// isSixDimSourceZero 判断六维判势数据源配置是否未设置（全部优先级为0），用于旧配置迁移。
// 优先级校验强制 >=1，故「全0」可靠表示该字段从未配置。
func isSixDimSourceZero(sc SixDimSourceConfig) bool {
	return sc.Northbound.Priority == 0 && sc.Margin.Priority == 0 &&
		sc.VolumeExpansion.Priority == 0 && sc.LimitBoard.Priority == 0 &&
		sc.FullMarketStats.Priority == 0 && sc.Overnight.Priority == 0
}

// FileLogEnabled 判断 log/ 日志文件是否启用。
// 该参数只保存在 config.json 中，仅允许用户手动修改，前端程序不提供修改入口；
// 供 main 在启动日志文件初始化之前调用（此时 ConfigManager 尚未创建）。配置不存在或
// 无法解析时默认返回 true（保持启用，行为与旧版本一致）。
func FileLogEnabled(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return true
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return true
	}
	v, ok := raw["enable_log_file"]
	if !ok {
		return true
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}
