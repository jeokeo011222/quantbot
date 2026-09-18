package data

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/version"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SQLiteManager SQLite 管理器
type SQLiteManager struct {
	db   *gorm.DB
	path string
}

// NewSQLiteManager 创建 SQLite 管理器
func NewSQLiteManager() (*SQLiteManager, error) {
	db, err := InitSQLite()
	if err != nil {
		return nil, err
	}
	return &SQLiteManager{
		db:   db,
		path: getDBPath(),
	}, nil
}

// NewSQLiteManagerFromDB 用给定的 gorm.DB 构造管理器（测试/嵌入式场景注入自定义库）。
func NewSQLiteManagerFromDB(db *gorm.DB) *SQLiteManager {
	return &SQLiteManager{db: db}
}

// GetDB 获取底层 gorm.DB
func (sm *SQLiteManager) GetDB() *gorm.DB {
	return sm.db
}

// Close 关闭数据库连接
func (sm *SQLiteManager) Close() {
	if sm.db != nil {
		sqlDB, err := sm.db.DB()
		if err == nil {
			sqlDB.Close()
		}
	}
}

func getDBPath() string {
	// 仅使用可执行文件目录下的database文件夹
	exePath, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("无法获取可执行文件路径: %v", err))
	}
	exeDir := filepath.Dir(exePath)
	exeDbDir := filepath.Join(exeDir, "database")
	os.MkdirAll(exeDbDir, 0755)
	return filepath.Join(exeDbDir, "quantpilot.db")
}

// getDBDir 获取数据库目录
func getDBDir() string {
	exePath, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("无法获取可执行文件路径: %v", err))
	}
	exeDir := filepath.Dir(exePath)
	exeDbDir := filepath.Join(exeDir, "database")
	os.MkdirAll(exeDbDir, 0755)
	return exeDbDir
}

// InitSQLite 初始化 SQLite 数据库
func InitSQLite() (*gorm.DB, error) {
	dbDir := getDBDir()
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	dbPath := filepath.Join(dbDir, "quantpilot.db")

	// 连接数据库
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SQLite: %w", err)
	}

	// 获取底层 SQL DB 进行连接池配置
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// 设置连接池参数
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetMaxOpenConns(1) // SQLite 单文件数据库，保持单连接
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 自动迁移 V1 MVP 核心表
	if err := db.AutoMigrate(
		&Instrument{},
		&User{},
		&SystemConfig{},
		&MarketIndex{},
		&WatchStock{},
		&Setting{},
		&Strategy{},
		&Agent{},
		&AgentSession{},
		&AgentToolCall{},
		&AgentDecision{},
		&Portfolio{},
		&Position{},
		&Order{},
		&Trade{},
		&CashFlow{},
		&RiskLimit{},
		&RiskCheck{},
		&PositionSnapshot{},
		&DecisionTrace{},
		// Investment Planner 相关表
		&InvestorProfile{},
		&Conversation{},
		&ConversationMessage{},
		&InvestmentPlan{},
		&StrategyCandidate{},
		&PlanApproval{},
		// AI Investment Team 相关表
		&AuditLog{},
		&PolicyLog{},
		&CIODecisionLog{},
		&AgentActivity{},
		&InvestmentMandate{},
		// 可交易股票池相关表
		&TradeableStock{},
		&TradeableStockLog{},
		// 仓位管理工具配置
		&PositionManagerConfig{},
		// 因子复盘质量画像
		&FactorQuality{},
		// 因子校准历史审计（EMA平滑/权重变更轨迹，对标 PanWatch FactorWeightHistory）
		&FactorCalibrationHistory{},
		// 回测相关表
		&BacktestResult{},
		&BacktestConfig{},
		// 每日复盘相关表
		&DailyReview{},
		// 策略日计划表
		&DailyStrategyPlan{},
		// 个股滚动调参覆盖表
		&StrategyStockParam{},
		// 智能体任务日志表
		&AgentTaskLog{},
		// 每日持仓快照表
		&PositionSnapshot{},
		&PortfolioDailyStat{},
		// 可证伪声明台账 + Alpha 候选库
		&ClaimRecord{},
		&AlphaStore{},
		// 研究中心-风险管理组合风险报告
		&MarketRiskReport{},
		// 投资组合中心-组合优化记录
		&PortfolioOptimization{},
	); err != nil {
		return nil, fmt.Errorf("failed to auto migrate: %w", err)
	}

	// 迁移：检查并添加 portfolios 表缺失的列
	migratePortfolioColumns(db)

	// 初始化默认数据
	initDefaultData(db)

	return db, nil
}

// ==================== 迁移函数 ====================

// migratePortfolioColumns 迁移 portfolios 表，添加缺失的列
func migratePortfolioColumns(db *gorm.DB) {
	requiredColumns := []struct {
		name       string
		definition string
	}{
		{"total_pnl", "REAL NOT NULL DEFAULT 0"},
		{"total_return", "REAL NOT NULL DEFAULT 0"},
		{"sharpe_ratio", "REAL DEFAULT 0"},
		{"max_drawdown", "REAL DEFAULT 0"},
	}

	for _, col := range requiredColumns {
		var count int
		err := db.Raw(
			"SELECT COUNT(*) FROM pragma_table_info('portfolios') WHERE name = ?",
			col.name,
		).Scan(&count).Error

		if err != nil {
			log.Printf("[Migration] Failed to check column %s in portfolios: %v", col.name, err)
			continue
		}

		if count == 0 {
			alterSQL := fmt.Sprintf("ALTER TABLE portfolios ADD COLUMN %s %s", col.name, col.definition)
			if err := db.Exec(alterSQL).Error; err != nil {
				log.Printf("[Migration] Failed to add column %s to portfolios: %v", col.name, err)
			} else {
				log.Printf("[Migration] Added column %s to portfolios successfully", col.name)
			}
		}
	}

	// 统一累计盈亏列：早期 gorm 结构体列名映射为 total_pn_l，而 updatePortfolio 以 "total_pnl" 直写，
	// 导致同一逻辑字段存在双列且数值分叉。此处在 total_pnl 为默认 0 时用 total_pn_l 回填（防丢历史盈亏），
	// 然后删除遗留的 total_pn_l 列，彻底消除「同一字段两个列」的错乱。
	var legacyCount int
	if err := db.Raw("SELECT COUNT(*) FROM pragma_table_info('portfolios') WHERE name = 'total_pn_l'").Scan(&legacyCount).Error; err == nil && legacyCount > 0 {
		if err := db.Exec("UPDATE portfolios SET total_pnl = total_pn_l WHERE total_pnl = 0 AND total_pn_l != 0").Error; err != nil {
			log.Printf("[Migration] 回填 total_pnl 失败: %v", err)
		}
		if err := db.Exec("ALTER TABLE portfolios DROP COLUMN total_pn_l").Error; err != nil {
			log.Printf("[Migration] 删除遗留列 total_pn_l 失败（可忽略）: %v", err)
		} else {
			log.Printf("[Migration] 已删除遗留重复列 total_pn_l（累计盈亏统一使用 total_pnl）")
		}
	}

	log.Printf("[Migration] Portfolio table columns verified")
}

// ==================== V1 MVP 数据模型 ====================

// Instrument 标的主数据表
type Instrument struct {
	ID            uint   `gorm:"primaryKey"`
	InstrumentID  string `gorm:"uniqueIndex;size:30;not null"` // 格式: 600519.SH
	Symbol        string `gorm:"index;size:20;not null"`
	Exchange      string `gorm:"index;size:10;not null"`
	Country       string `gorm:"size:5;not null"`
	Currency      string `gorm:"size:3;not null;default:CNY"`
	Name          string `gorm:"size:100;not null"`
	AssetType     string `gorm:"size:20;not null;default:equity"`
	Sector        string `gorm:"size:50"`
	Industry      string `gorm:"size:50"`
	ListingDate   *time.Time
	DelistingDate *time.Time
	IsActive      int `gorm:"not null;default:1"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// User 用户表
type User struct {
	ID        uint   `gorm:"primaryKey"`
	Username  string `gorm:"uniqueIndex;size:50;not null"`
	Email     string `gorm:"size:100"`
	Tier      string `gorm:"size:20;not null;default:free"` // free/pro/enterprise
	Status    string `gorm:"size:20;not null;default:active"`
	ExpireAt  *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SystemConfig 系统配置表（版本信息、关于等）
type SystemConfig struct {
	ID           uint   `gorm:"primaryKey"`
	AppVersion   string `gorm:"size:20;not null;default:1.0.0"`
	AppName      string `gorm:"size:100;not null;default:QuantBot AI"`
	AboutText    string `gorm:"type:text"`
	FeaturesJSON string `gorm:"type:text"` // 功能列表JSON
	UpdatedAt    time.Time
}

// MarketIndex 市场指数配置表
type MarketIndex struct {
	ID        uint   `gorm:"primaryKey"`
	Code      string `gorm:"uniqueIndex;size:20;not null"` // sh000001
	Name      string `gorm:"size:100;not null"`
	Market    string `gorm:"index;size:10;not null"` // SH/SZ
	IndexType string `gorm:"size:30;not null"`       // COMPOSITE/SME/CHINEXT
	SortOrder int    `gorm:"default:0"`
	IsActive  int    `gorm:"not null;default:1"`
	CreatedAt time.Time
}

// WatchStock 监控股票表（替代硬编码的默认监控列表）
type WatchStock struct {
	ID        uint   `gorm:"primaryKey"`
	Code      string `gorm:"uniqueIndex;size:20;not null"` // 纯数字代码，如 600519
	Name      string `gorm:"size:100;not null"`
	Market    string `gorm:"index;size:10;not null"` // sh/sz/bj
	Sector    string `gorm:"index;size:50"`          // 行业/板块
	SortOrder int    `gorm:"default:0"`
	IsActive  int    `gorm:"not null;default:1"`
	CreatedAt time.Time
}

// Setting 系统设置表
type Setting struct {
	ID uint `gorm:"primaryKey"`

	// 市场配置
	Market      string `gorm:"size:10;not null;default:CN"`
	TradingMode string `gorm:"size:20;not null;default:paper"`

	// AI 配置
	AIProvider  string `gorm:"size:30;not null;default:deepseek"`
	AIModel     string `gorm:"size:50;not null;default:deepseek-v4-flash"`
	AIBaseURL   string `gorm:"size:200;not null;default:https://api.deepseek.com"`
	AIAPIKeyRef string `gorm:"size:100"` // DPAPI 加密引用

	// RiskOS 配置
	RiskOSEnabled   int    `gorm:"not null;default:0"`
	RiskOSAPIKeyRef string `gorm:"size:100"`

	// 数据源
	DataProvider string `gorm:"size:30;not null;default:tdx_local"`

	// Broker
	BrokerType         string `gorm:"size:30;not null;default:paper"`
	BrokerAPIKeyRef    string `gorm:"size:100"`
	BrokerAPISecretRef string `gorm:"size:100"`

	// 交易参数
	InitialCapital float64 `gorm:"type:decimal(18,2);not null;default:100000"`
	Currency       string  `gorm:"size:3;not null;default:CNY"`

	// 运行时
	AutoDailyRun int    `gorm:"not null;default:1"`
	DailyRunTime string `gorm:"size:5;not null;default:09:30"`

	UpdatedAt time.Time
}

// Strategy 策略表
type Strategy struct {
	ID                 uint       `gorm:"primaryKey"`
	Name               string     `gorm:"index;size:100;not null"`
	Description        string     `gorm:"type:text"`
	StrategyType       string     `gorm:"index;size:30;not null"`
	IsActive           int        `gorm:"index;not null;default:1"`
	IsBuiltin          int        `gorm:"index;not null;default:0"` // 内置策略标识
	ConfigJSON         string     `gorm:"type:text"`                // JSON 格式配置（当前生效参数）
	UniverseJSON       string     `gorm:"type:text"`                // JSON 格式标的池
	TuneRangesJSON     string     `gorm:"type:text"`                // JSON 格式参数扫描范围（自动化调参工具使用）
	LastTunedAt        *time.Time // 最近一次自动调参时间
	IsAIGenerated      int        `gorm:"not null;default:0"`
	AIPrompt           string     `gorm:"type:text"`
	BacktestResultJSON string     `gorm:"type:text"`

	// 策略表现指标（来自最近回测）
	SharpeRatio float64 `gorm:"type:decimal(10,4);default:0"` // 夏普比率
	MaxDrawdown float64 `gorm:"type:decimal(10,4);default:0"` // 最大回撤(%)
	TotalReturn float64 `gorm:"type:decimal(10,4);default:0"` // 总收益率(%)
	WinRate     float64 `gorm:"type:decimal(10,4);default:0"` // 胜率(%)
	Turnover    float64 `gorm:"type:decimal(10,4);default:0"` // 年化换手率(单位资本年成交倍数)

	// 策略参数
	InitialCapital float64 `gorm:"type:decimal(18,2);default:100000"` // 初始资金
	MaxPosition    int     `gorm:"default:10"`                        // 最大持仓数
	StopLossPct    float64 `gorm:"type:decimal(6,4);default:0.0500"`  // 止损比例
	TakeProfitPct  float64 `gorm:"type:decimal(6,4);default:0.2000"`  // 止盈比例

	CreatedAt time.Time
	UpdatedAt time.Time
}

// FactorQuality 因子复盘质量画像：最近一次 factor_review 计算得到的各因子质量分([0,1])，
// 由 nightly_quant 量化复盘每日落库。选股时用于对默认因子权重做自适应加权（好因子上调、差因子下调），
// 质量分随盘面阶段每夜重算，从而体现“不同阶段因子价值不同”。
// 质量分经 EMA 平滑（防单日IC噪声跳变）；RankIC/Accuracy 等为最近一次真实复盘原始值（供审计展示）；
// IsPinned/AutoCalibrate 提供 pin 覆盖能力（对标 PanWatch FactorWeight.is_pinned / auto_calibrate）。
type FactorQuality struct {
	FactorName string  `gorm:"primaryKey;size:40"`
	Quality    float64 `gorm:"type:decimal(6,4);default:0"` // 因子质量分 0~1（EMA平滑后）
	Detail     string  `gorm:"type:text"`                   // 溯源说明（复盘维度摘要）
	// 最近一次真实复盘原始指标（未平滑，供审计与API展示）
	RankIC    float64 `gorm:"type:decimal(8,4);default:0"` // 最近一次 |RankIC|
	Accuracy  float64 `gorm:"type:decimal(6,4);default:0"` // 最上1/3前瞻命中率
	Coverage  float64 `gorm:"type:decimal(6,2);default:0"` // 覆盖率 %
	Stability float64 `gorm:"type:decimal(8,4);default:0"` // 横截面离散度 std
	Decay     float64 `gorm:"type:decimal(8,4);default:0"` // RankIC(20日)-RankIC(1日)
	// pin 覆盖：IsPinned=true 或 AutoCalibrate=false 时跳过自动校准（保留当前质量分，只刷新观测指标）
	IsPinned      bool `gorm:"default:false"`
	AutoCalibrate bool `gorm:"default:true"`
	// 诚实下线：分半稳定性检验（split_half_stable=false 视为可疑假阳性，选股时额外降权）；
	// SplitHalfIC 记录两半段 RankIC（供审计追溯）
	SplitHalfStable bool   `gorm:"default:false"`
	SplitHalfIC     string `gorm:"size:40"` // "ic1/ic2"，最近一次分半 IC
	UpdatedAt       time.Time
}

// FactorCalibrationHistory 因子校准历史审计：记录每次自动校准的质量分变化与原始观测指标，
// 与 FactorQuality 一一对应，形成可追溯的校准轨迹（对标 PanWatch FactorWeightHistory）。
type FactorCalibrationHistory struct {
	ID         uint    `gorm:"primaryKey"`
	FactorName string  `gorm:"index;size:40"`
	OldQuality float64 `gorm:"type:decimal(6,4)"` // 校准前质量分（EMA平滑后）
	NewQuality float64 `gorm:"type:decimal(6,4)"` // 校准后质量分
	RankIC     float64 `gorm:"type:decimal(8,4)"` // 触发本次校准的原始 |RankIC|
	Accuracy   float64 `gorm:"type:decimal(6,4)"`
	Coverage   float64 `gorm:"type:decimal(6,2)"`
	Stability  float64 `gorm:"type:decimal(8,4)"`
	Decay      float64 `gorm:"type:decimal(8,4)"`
	Reason     string  `gorm:"size:30"` // auto / seeded / pinned_skipped / disabled_skipped
	CreatedAt  time.Time
}

// DailyStrategyPlan 策略日计划表：量化分析师每日收盘后做因子复盘、选定次日交易策略，
// 写入本表；操盘手次日盘前/盘中据此执行对应策略信号（如 KDJ 死叉卖出）。
// 同一交易日( trade_date )只有一行，后续选择会覆盖更新。
type DailyStrategyPlan struct {
	ID              uint   `gorm:"primaryKey"`
	TradeDate       string `gorm:"index;size:10;not null;uniqueIndex"` // 计划生效交易日 YYYY-MM-DD
	StrategyType    string `gorm:"index;size:30;not null"`             // 策略类型 kdj_golden / ma_cross / ...
	StrategyName    string `gorm:"size:100;not null"`                  // 策略名称（可读）
	Reason          string `gorm:"type:text"`                          // 选择理由（因子复盘摘要）
	Status          string `gorm:"size:20;default:active"`             // active / closed
	ExecutedSellNum int    `gorm:"default:0"`                          // 当日已按该策略执行卖出信号次数
	CreatedBy       string `gorm:"size:50"`                            // 选择人（quant/auto）
	Source          string `gorm:"size:20;default:auto"`               // quant=量化分析师手动 / auto=盘后自动兑底
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// StrategyStockParam 个股策略绑定与参数覆盖表：收盘后对每只持仓按该股自身近期日K线（如250日）
// 为每套活跃策略评估表现，选出一套最适配的策略作为该股的绑定策略，并将其调优参数写入本表
// （每股一行）。次日 CIO/操盘手在该股上计算策略信号时，按该股绑定的策略类型与参数执行。
// code 唯一，每个交易日盘后滚动刷新（绑定策略可能随行情改变）。
type StrategyStockParam struct {
	ID           uint    `gorm:"primaryKey"`
	Code         string  `gorm:"size:20;not null;uniqueIndex"` // 个股规范符号，如 sh600519（每股仅一行绑定）
	StrategyType string  `gorm:"size:30;not null"`             // 该股绑定策略类型 kdj_golden / ma_cross / ...
	StrategyName string  `gorm:"size:100"`                     // 策略名称（可读）
	ParamsJSON   string  `gorm:"type:text"`                    // 该股绑定策略调优后的参数（JSON）
	WindowDays   int     `gorm:"default:250"`                  // 调参回看交易日窗口
	SharpeRatio  float64 `gorm:"type:decimal(8,4)"`            // 该股绑定策略在个股上的夏普
	TotalReturn  float64 `gorm:"type:decimal(10,4)"`           // 该股绑定策略在个股上的总收益(%)
	WinRate      float64 `gorm:"type:decimal(6,4)"`            // 该股绑定策略在个股上的胜率
	MaxDrawdown  float64 `gorm:"type:decimal(8,4)"`            // 该股绑定策略在个股上的最大回撤(%)
	Trades       int     `gorm:"default:0"`                    // 该股绑定策略在个股上的交易次数
	UpdatedAt    time.Time
}

// PositionManagerConfig 仓位管理工具配置表（单行，config_json 为唯一数据源）。
// 参数可通过 position_manager 工具的 set_config 动作更新，不写死在代码中。
type PositionManagerConfig struct {
	ID         uint   `gorm:"primaryKey"`
	Name       string `gorm:"size:50;not null;default:default;uniqueIndex"` // 配置名称，默认 default
	ConfigJSON string `gorm:"type:text;not null"`                           // 仓位策略完整参数（JSON）
	UpdatedAt  time.Time
}

// Agent Agent 配置表
type Agent struct {
	ID             uint    `gorm:"primaryKey"`
	AgentID        string  `gorm:"uniqueIndex;size:50;not null"`
	Name           string  `gorm:"size:100;not null"`
	Description    string  `gorm:"type:text"`
	AgentType      string  `gorm:"size:30;not null"`
	DecisionWeight float64 `gorm:"type:decimal(5,4);not null;default:0.2000"`
	SystemPrompt   string  `gorm:"type:text;not null"`
	ToolListJSON   string  `gorm:"type:text;not null"`
	IsActive       int     `gorm:"not null;default:1"`
	HealthScore    float64 `gorm:"type:decimal(5,2);default:70.00"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AgentSession Agent 会话表
type AgentSession struct {
	ID              uint      `gorm:"primaryKey"`
	SessionID       string    `gorm:"uniqueIndex;size:50;not null"`
	SessionDate     time.Time `gorm:"index;not null"`
	Status          string    `gorm:"index;size:20;not null"`
	StartTime       *time.Time
	EndTime         *time.Time
	DurationMs      int
	Summary         string  `gorm:"type:text"`
	Confidence      float64 `gorm:"type:decimal(5,4)"`
	TokenUsage      int
	DecisionTraceID string `gorm:"size:50"`
	CreatedAt       time.Time
}

// AgentToolCall 工具调用表
type AgentToolCall struct {
	ID             uint   `gorm:"primaryKey"`
	SessionID      string `gorm:"index;size:50;not null"`
	AgentID        string `gorm:"index;size:50;not null"`
	ToolName       string `gorm:"index;size:50;not null"`
	ToolCategory   string `gorm:"size:30;not null"`
	InputJSON      string `gorm:"type:text"`
	OutputJSON     string `gorm:"type:text"`
	CallStatus     string `gorm:"index;size:20;not null"`
	ErrorMsg       string `gorm:"type:text"`
	CallDurationMs int
	TokenUsage     int
	CreatedAt      time.Time
}

// AgentDecision Agent 决策表
type AgentDecision struct {
	ID              uint    `gorm:"primaryKey"`
	SessionID       string  `gorm:"index;size:50;not null"`
	AgentID         string  `gorm:"index;size:50;not null"`
	DecisionTraceID string  `gorm:"size:50"`
	DecisionType    string  `gorm:"index;size:30;not null"`
	ResultJSON      string  `gorm:"type:text;not null"`
	Confidence      float64 `gorm:"type:decimal(5,4)"`
	Interpretation  string  `gorm:"type:text"`
	CreatedAt       time.Time
}

// Portfolio 组合表
type Portfolio struct {
	ID             uint   `gorm:"primaryKey"`
	Name           string `gorm:"size:100;not null"`
	PortfolioType  string `gorm:"size:30;not null"`
	StrategyID     *uint
	InitialCapital float64 `gorm:"type:decimal(18,2);not null"`
	CurrentCapital float64 `gorm:"type:decimal(18,2);not null"`
	Cash           float64 `gorm:"type:decimal(18,2);not null"`
	// column:total_pnl —— 统一累计盈亏列：updatePortfolio 等以 "total_pnl" 直写，
	// 结构体也显式映射到 total_pnl，杜绝与 gorm 默认 total_pn_l 双列分叉导致数据错乱。
	TotalPnL    float64 `gorm:"column:total_pnl;type:decimal(18,2);not null;default:0"`
	TotalReturn float64 `gorm:"type:decimal(10,6);not null;default:0"`
	SharpeRatio float64 `gorm:"type:decimal(8,4)"`
	MaxDrawdown float64 `gorm:"type:decimal(10,6)"`
	IsActive    int     `gorm:"not null;default:1"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Position 持仓表
type Position struct {
	ID               uint    `gorm:"primaryKey"`
	PortfolioID      uint    `gorm:"index;not null"`
	InstrumentID     string  `gorm:"index;size:30;not null"`
	Quantity         int     `gorm:"not null"`
	AvgCost          float64 `gorm:"type:decimal(12,4);not null"`
	CurrentPrice     float64 `gorm:"type:decimal(12,4)"`
	MarketValue      float64 `gorm:"type:decimal(18,2)"`
	UnrealizedPnL    float64 `gorm:"type:decimal(18,2)"`
	UnrealizedReturn float64 `gorm:"type:decimal(10,6)"`
	Weight           float64 `gorm:"type:decimal(8,6)"`
	PositionStatus   string  `gorm:"index;size:20;not null;default:open"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Order 订单表
type Order struct {
	ID              uint    `gorm:"primaryKey"`
	OrderID         string  `gorm:"uniqueIndex;size:50;not null"`
	PortfolioID     uint    `gorm:"index;not null"`
	InstrumentID    string  `gorm:"index;size:30;not null"`
	DecisionTraceID string  `gorm:"size:50"`
	OrderType       string  `gorm:"size:20;not null"`
	Side            string  `gorm:"size:10;not null"`
	Quantity        int     `gorm:"not null"`
	Price           float64 `gorm:"type:decimal(12,4)"`
	LimitPrice      float64 `gorm:"type:decimal(12,4)"`
	StopPrice       float64 `gorm:"type:decimal(12,4)"`
	Status          string  `gorm:"index;size:20;not null"`
	FilledQuantity  int
	FilledPrice     float64 `gorm:"type:decimal(12,4)"`
	// IdempotencyKey 幂等键：同一决策+标的+方向+数量确定性重复提交时唯一约束拦截，
	// 防止审批补确认/网络重试/调度重触发造成重复下单。
	IdempotencyKey string `gorm:"uniqueIndex;size:100"`
	RiskCheckID    *uint
	RiskApproved   int `gorm:"not null;default:0"`
	SubmittedAt    *time.Time
	FilledAt       *time.Time
	CancelledAt    *time.Time
	CreatedAt      time.Time
}

// CashFlow 资金流水表（账本闭环：每笔现金变动独立留痕，可独立对账，不依赖从 Trade 反推）
type CashFlow struct {
	ID           uint    `gorm:"primaryKey"`
	PortfolioID  uint    `gorm:"index;not null"`
	TradeID      string  `gorm:"index;size:50"`
	OrderID      string  `gorm:"index;size:50"`
	InstrumentID string  `gorm:"index;size:30"`
	Type         string  `gorm:"size:20;not null"`            // buy / sell / deposit / withdraw / dividend / fee / adjust
	Side         string  `gorm:"size:10"`                     // inflow(资金流入) / outflow(资金流出)
	Amount       float64 `gorm:"type:decimal(18,2);not null"` // 变动金额（正的绝对值）
	NetAmount    float64 `gorm:"type:decimal(18,2);not null"` // 净变动，买入为负、卖出/入金为正
	BalanceAfter float64 `gorm:"type:decimal(18,2);not null"` // 变动后现金余额
	Reason       string  `gorm:"size:255"`
	CreatedAt    time.Time
}

// Trade 成交表
type Trade struct {
	ID           uint      `gorm:"primaryKey"`
	TradeID      string    `gorm:"uniqueIndex;size:50;not null"`
	OrderID      string    `gorm:"size:50;not null"`
	PortfolioID  uint      `gorm:"index;not null"`
	InstrumentID string    `gorm:"index;size:30;not null"`
	Side         string    `gorm:"size:10;not null"`
	Quantity     int       `gorm:"not null"`
	Price        float64   `gorm:"type:decimal(12,4);not null"`
	GrossAmount  float64   `gorm:"type:decimal(18,2);not null"`
	Commission   float64   `gorm:"type:decimal(18,2);not null;default:0"`
	Fees         float64   `gorm:"type:decimal(18,2);not null;default:0"`
	NetAmount    float64   `gorm:"type:decimal(18,2);not null"`
	RealizedPnL  float64   `gorm:"type:decimal(18,2)"`
	DecisionID   string    `gorm:"index;size:50"` // 关联的DecisionObject决策ID（Trader只执行合法决策）
	TradeDate    time.Time `gorm:"index;not null"`
	TradedAt     *time.Time
	CreatedAt    time.Time
}

// RiskLimit 风险限制表
type RiskLimit struct {
	ID                       uint `gorm:"primaryKey"`
	PortfolioID              *uint
	MaxPositionPct           float64 `gorm:"type:decimal(5,4);not null;default:0.3000"`
	MaxTotalExposurePct      float64 `gorm:"type:decimal(5,4);not null;default:1.0000"`
	MaxLeverage              float64 `gorm:"type:decimal(5,4);not null;default:1.5000"`
	MaxDailyLossPct          float64 `gorm:"type:decimal(5,4);not null;default:0.0500"`
	MaxDrawdownPct           float64 `gorm:"type:decimal(5,4);not null;default:0.2000"`
	MaxOrderSize             int     `gorm:"not null;default:100000"`
	MaxDailyTurnoverPct      float64 `gorm:"type:decimal(5,4);not null;default:1.0000"`
	TradingHoursStart        string  `gorm:"size:5;not null;default:09:30"`
	TradingHoursEnd          string  `gorm:"size:5;not null;default:16:00"`
	AllowTradingOutsideHours int     `gorm:"not null;default:0"`
	IsActive                 int     `gorm:"not null;default:1"`
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// RiskCheck 风险检查表
type RiskCheck struct {
	ID                  uint    `gorm:"primaryKey"`
	CheckID             string  `gorm:"uniqueIndex;size:50;not null"`
	OrderID             string  `gorm:"index;size:50"`
	DecisionTraceID     string  `gorm:"size:50"`
	IsApproved          int     `gorm:"index;not null;default:0"`
	CheckType           string  `gorm:"size:30;not null"`
	ChecksJSON          string  `gorm:"type:text;not null"`
	FailedChecksJSON    string  `gorm:"type:text"`
	CurrentExposurePct  float64 `gorm:"type:decimal(8,4)"`
	CurrentDrawdownPct  float64 `gorm:"type:decimal(8,4)"`
	CurrentDailyLossPct float64 `gorm:"type:decimal(8,4)"`
	CheckedAt           time.Time
}

// DecisionTrace 决策轨迹表
type DecisionTrace struct {
	ID                 uint      `gorm:"primaryKey"`
	TraceID            string    `gorm:"uniqueIndex;size:50;not null"`
	SessionID          string    `gorm:"size:50;not null"`
	DecisionDate       time.Time `gorm:"index;not null"`
	MarketRegime       string    `gorm:"size:30"`
	MarketConfidence   float64   `gorm:"type:decimal(5,4)"`
	AlphaSummary       string    `gorm:"type:text"`
	AlphaSignalsJSON   string    `gorm:"type:text"`
	RiskSummary        string    `gorm:"type:text"`
	RiskAssessmentJSON string    `gorm:"type:text"`
	PortfolioSummary   string    `gorm:"type:text"`
	PortfolioPlanJSON  string    `gorm:"type:text"`
	FinalDecision      string    `gorm:"type:text;not null"`
	TargetExposurePct  float64   `gorm:"type:decimal(5,4)"`
	TraceStepsJSON     string    `gorm:"type:text;not null"`
	RiskGatePassed     int       `gorm:"not null;default:0"`
	RiskGateNotes      string    `gorm:"type:text"`
	CreatedAt          time.Time
}

// ==================== Investment Planner 数据模型 ====================

// InvestorProfile 投资者画像表
type InvestorProfile struct {
	ID                   uint    `gorm:"primaryKey"`
	UserID               uint    `gorm:"index;not null"`
	ProfileJSON          string  `gorm:"type:text;not null"` // 完整画像 JSON
	Capital              float64 `gorm:"type:decimal(18,2)"`
	Currency             string  `gorm:"size:3;default:CNY"`
	InvestmentExperience string  `gorm:"size:30"` // NOVICE/INTERMEDIATE/EXPERIENCED/SENIOR
	InvestmentStyle      string  `gorm:"size:30"` // VALUE/GROWTH/DIVIDEND/QUALITY/INDEX/BALANCED
	InvestmentHorizon    string  `gorm:"size:30"` // SHORT/MEDIUM/LONG/VERY_LONG
	InvestmentObjective  string  `gorm:"size:30"` // GROWTH/INCOME/BALANCED/PRESERVATION
	RiskTolerance        string  `gorm:"size:20"` // R1/R2/R3/R4/R5
	DrawdownTolerance    float64 `gorm:"type:decimal(5,4)"`
	LossTolerance        float64 `gorm:"type:decimal(5,4)"`
	LiquidityRequirement string  `gorm:"size:20"`                     // LOW/MEDIUM/HIGH
	TradingFrequency     string  `gorm:"size:20"`                     // LOW/MEDIUM/HIGH
	MarketPreference     string  `gorm:"size:10;default:CN"`          // 系统仅支持A股(CN)
	ProfileCompleteness  float64 `gorm:"type:decimal(5,4);default:0"` // 信息完整度 0-1
	CurrentStep          string  `gorm:"size:30"`                     // WELCOME/INTERVIEW/PLANNING/REVIEW/COMPLETE
	StepProgress         int     `gorm:"default:0"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Conversation 对话表
type Conversation struct {
	ID             uint   `gorm:"primaryKey"`
	ConversationID string `gorm:"uniqueIndex;size:50;not null"`
	UserID         uint   `gorm:"index;not null"`
	ProfileID      *uint
	Title          string `gorm:"size:200"`
	Status         string `gorm:"index;size:20;not null;default:active"` // active/completed/archived
	CurrentStep    string `gorm:"size:30"`
	MessageCount   int    `gorm:"default:0"`
	StartedAt      time.Time
	UpdatedAt      time.Time
}

// ConversationMessage 对话消息表
type ConversationMessage struct {
	ID             uint   `gorm:"primaryKey"`
	ConversationID string `gorm:"index;size:50;not null"`
	Role           string `gorm:"index;size:20;not null"` // user/assistant/system
	Content        string `gorm:"type:text;not null"`
	MessageType    string `gorm:"size:30"`   // text/question/answer/action
	MetadataJSON   string `gorm:"type:text"` // 附加数据，如问题ID、选项等
	CreatedAt      time.Time
}

// InvestmentPlan 投资计划表
type InvestmentPlan struct {
	ID               uint    `gorm:"primaryKey"`
	PlanID           string  `gorm:"uniqueIndex;size:50;not null"`
	UserID           uint    `gorm:"index;not null"`
	ProfileID        uint    `gorm:"index;not null"`
	Name             string  `gorm:"size:100;not null"`
	Objective        string  `gorm:"size:200"`
	RiskLevel        string  `gorm:"size:20"` // conservative/balanced/growth
	TargetReturn     float64 `gorm:"type:decimal(8,4)"`
	TargetVolatility float64 `gorm:"type:decimal(8,4)"`
	MaxDrawdown      float64 `gorm:"type:decimal(8,4)"`
	StrategyType     string  `gorm:"size:30"`
	Status           string  `gorm:"index;size:30;not null;default:DRAFT"` // DRAFT/GENERATING/REVIEWING/PENDING_APPROVAL/APPROVED/ACTIVE/RUNNING/PAUSED/REJECTED/ARCHIVED
	Version          int     `gorm:"default:1"`
	PlanJSON         string  `gorm:"type:text"` // 完整计划 JSON
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// StrategyCandidate 策略候选方案表
type StrategyCandidate struct {
	ID                 uint    `gorm:"primaryKey"`
	CandidateID        string  `gorm:"uniqueIndex;size:50;not null"`
	PlanID             string  `gorm:"index;size:50;not null"`
	Name               string  `gorm:"size:100;not null"`
	Description        string  `gorm:"type:text"`
	Label              string  `gorm:"size:30"` // A/B/C 对应稳健/平衡/增长
	Universe           string  `gorm:"type:text"`
	SignalDefinition   string  `gorm:"type:text"`
	ExpectedReturn     float64 `gorm:"type:decimal(8,4)"`
	ExpectedVolatility float64 `gorm:"type:decimal(8,4)"`
	MaxDrawdown        float64 `gorm:"type:decimal(8,4)"`
	Sharpe             float64 `gorm:"type:decimal(8,4)"`
	Calmar             float64 `gorm:"type:decimal(8,4)"`
	BacktestStart      *time.Time
	BacktestEnd        *time.Time
	TransactionCost    float64 `gorm:"type:decimal(8,6)"`
	Turnover           float64 `gorm:"type:decimal(8,4)"`
	RobustnessScore    float64 `gorm:"type:decimal(5,4)"`
	RiskScore          float64 `gorm:"type:decimal(5,4)"`
	RiskOSStatus       string  `gorm:"size:20"` // PASSED/WARNING/BLOCKED
	Status             string  `gorm:"index;size:20;not null;default:DRAFT"`
	CandidateJSON      string  `gorm:"type:text"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// PlanApproval 计划审批表
type PlanApproval struct {
	ID               uint   `gorm:"primaryKey"`
	PlanID           string `gorm:"uniqueIndex;size:50;not null"`
	UserID           uint   `gorm:"index;not null"`
	IsApproved       int    `gorm:"index;not null;default:0"`
	RiskAcknowledged int    `gorm:"not null;default:0"`
	Notes            string `gorm:"type:text"`
	ApprovedAt       *time.Time
	CreatedAt        time.Time
}

// ==================== AI Investment Team 数据模型 ====================

// AuditLog 审计日志表（系统级审计，记录所有关键操作）
type AuditLog struct {
	ID          uint      `gorm:"primaryKey"`
	EventID     string    `gorm:"uniqueIndex;size:36;not null"`
	EventType   string    `gorm:"index;size:50;not null"`
	UserID      string    `gorm:"index;size:50"`
	UserName    string    `gorm:"size:50"`
	Action      string    `gorm:"index;size:100;not null"`
	TargetType  string    `gorm:"index;size:50"`
	TargetID    string    `gorm:"size:100"`
	Result      string    `gorm:"index;size:20;not null"` // success / failed / warning
	DetailsJSON string    `gorm:"type:text"`
	IPAddress   string    `gorm:"size:50"`
	Timestamp   time.Time `gorm:"index;not null"`
}

// 审计事件类型常量
const (
	AuditEventLogin          = "LOGIN"
	AuditEventLogout         = "LOGOUT"
	AuditEventScreening      = "SCREENING"
	AuditEventStrategy       = "STRATEGY"
	AuditEventBacktest       = "BACKTEST"
	AuditEventAIAnalysis     = "AI_ANALYSIS"
	AuditEventPlanner        = "PLANNER"
	AuditEventLiveActivity   = "LIVE_ACTIVITY"
	AuditEventCIODailyReview = "CIO_DAILY_REVIEW"
	AuditEventApproval       = "APPROVAL"
	AuditEventOrder          = "ORDER"
	AuditEventConfigChange   = "CONFIG_CHANGE"
	AuditEventTierChange     = "TIER_CHANGE"
)

// PolicyLog Policy引擎日志表
type PolicyLog struct {
	ID          uint      `gorm:"primaryKey"`
	Action      string    `gorm:"index;size:50;not null"`
	DetailsJSON string    `gorm:"type:text"`
	Timestamp   time.Time `gorm:"index;not null"`
}

// CIODecisionLog CIO决策记录表
type CIODecisionLog struct {
	ID               uint      `gorm:"primaryKey"`
	DecisionID       string    `gorm:"uniqueIndex;size:50;not null"`
	PortfolioID      string    `gorm:"index;size:50"`
	Decision         string    `gorm:"index;size:30;not null"`
	Reason           string    `gorm:"type:text"`
	OrdersJSON       string    `gorm:"type:text"`
	RiskApproval     string    `gorm:"size:20"`
	PolicyStatus     string    `gorm:"size:20"`
	MarketState      string    `gorm:"size:30"`
	MarketConfidence float64   `gorm:"type:decimal(5,4)"`
	FactorScoresJSON string    `gorm:"type:text"`
	QuantReportJSON  string    `gorm:"type:text"`
	RiskReportJSON   string    `gorm:"type:text"`
	ExecutionResult  string    `gorm:"type:text"`
	DecisionProcess  string    `gorm:"type:text"`
	Timestamp        time.Time `gorm:"index;not null"`
}

// AgentActivity Agent活动表（用于Activity页面）
type AgentActivity struct {
	ID           uint      `gorm:"primaryKey"`
	AgentRole    string    `gorm:"index;size:20;not null"`
	ActivityType string    `gorm:"index;size:30;not null"`
	Title        string    `gorm:"size:200"`
	Message      string    `gorm:"type:text"`
	MetadataJSON string    `gorm:"type:text"`
	Timestamp    time.Time `gorm:"index;not null"`
}

// InvestmentMandate 投资指令表（用户确认后的正式投资计划）
type InvestmentMandate struct {
	ID                 uint    `gorm:"primaryKey"`
	MandateID          string  `gorm:"uniqueIndex;size:50;not null"`
	UserID             uint    `gorm:"index;not null"`
	Capital            float64 `gorm:"type:decimal(18,2)"`
	Currency           string  `gorm:"size:3;default:CNY"`
	Horizon            string  `gorm:"size:30"`
	Objective          string  `gorm:"size:50"`
	RiskTolerance      string  `gorm:"size:20"`
	MaxDrawdown        float64 `gorm:"type:decimal(5,4)"`
	LiquidityReq       string  `gorm:"size:20"`
	TradingFreq        string  `gorm:"size:20"`
	ManualIntervention string  `gorm:"size:20"`
	Status             string  `gorm:"index;size:20;not null;default:ACTIVE"`
	Version            int     `gorm:"default:1"`
	ConfirmedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ==================== 可交易股票池数据模型 ====================

// TradeableStock 可交易股票表（用户通过选股引擎选出并提交的股票）
type TradeableStock struct {
	ID               uint       `gorm:"primaryKey"`
	StockCode        string     `gorm:"uniqueIndex;size:20;not null"` // 股票代码
	StockName        string     `gorm:"size:100;not null"`            // 股票名称
	Market           string     `gorm:"index;size:10;not null"`       // 市场：SH/SZ/BJ
	CurrentPrice     float64    `gorm:"type:decimal(12,4)"`           // 当前价格
	TargetPrice      float64    `gorm:"type:decimal(12,4)"`           // 目标价格
	StopLossPrice    float64    `gorm:"type:decimal(12,4)"`           // 止损价格
	CompositeScore   float64    `gorm:"type:decimal(5,4)"`            // 综合评分 0-1
	FactorScoresJSON string     `gorm:"type:text"`                    // 各因子评分 JSON
	SelectionReason  string     `gorm:"type:text"`                    // 选股理由
	RiskWarning      string     `gorm:"type:text"`                    // 风险提示
	SelectionSource  string     `gorm:"index;size:50"`                // 选股来源：SCREENER/AI_AGENT/MANUAL
	StrategyID       string     `gorm:"size:50"`                      // 策略ID
	PlanID           string     `gorm:"index;size:50"`                // 关联投资方案ID（股票池来源）
	FactorVersion    string     `gorm:"size:50"`                      // 因子版本
	MarketDate       *time.Time // 市场数据日期
	Status           string     `gorm:"index;size:20;not null;default:PENDING"` // PENDING/APPROVED/REJECTED/BOUGHT/SOLD/EXPIRED
	Priority         int        `gorm:"default:3"`                              // 优先级：1-5，1最高
	SuggestedWeight  float64    `gorm:"type:decimal(5,4)"`                      // 建议仓位权重
	MaxPositionPct   float64    `gorm:"type:decimal(5,4);default:0.1000"`       // 最大单票仓位
	MinPositionPct   float64    `gorm:"type:decimal(5,4);default:0.0100"`       // 最小单票仓位
	OrderType        string     `gorm:"size:20;default:LIMIT"`                  // 订单类型：MARKET/LIMIT
	SubmitterID      string     `gorm:"size:50"`                                // 提交人ID
	SubmitterName    string     `gorm:"size:50"`                                // 提交人名称
	SubmittedAt      time.Time  `gorm:"index;not null"`                         // 提交时间
	ReviewedAt       *time.Time // 审核时间
	ReviewerID       string     `gorm:"size:50"`            // 审核人ID
	ReviewerName     string     `gorm:"size:50"`            // 审核人名称
	ReviewComment    string     `gorm:"type:text"`          // 审核意见
	ApprovedQuantity int        `gorm:"default:0"`          // 批准数量
	BoughtQuantity   int        `gorm:"default:0"`          // 已购买数量
	BoughtPrice      float64    `gorm:"type:decimal(12,4)"` // 购买价格
	BoughtAt         *time.Time // 购买时间
	TargetDate       *time.Time // 目标完成日期
	ExpireAt         *time.Time // 过期时间
	Tags             string     `gorm:"size:500"` // 标签（逗号分隔）
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TradeableStockLog 可交易股票操作日志表
type TradeableStockLog struct {
	ID           uint   `gorm:"primaryKey"`
	StockCode    string `gorm:"index;size:20;not null"`
	Action       string `gorm:"index;size:30;not null"` // SUBMIT/APPROVE/REJECT/BUY/SELL/CANCEL
	FromStatus   string `gorm:"size:20"`
	ToStatus     string `gorm:"size:20"`
	OperatorID   string `gorm:"size:50"`
	OperatorName string `gorm:"size:50"`
	Comment      string `gorm:"type:text"`
	DetailsJSON  string `gorm:"type:text"`
	CreatedAt    time.Time
}

// ==================== 回测相关模型 ====================

// BacktestResult 回测结果表
type BacktestResult struct {
	ID             uint      `gorm:"primaryKey"`
	StrategyID     string    `gorm:"index;size:50;not null"`                   // 关联策略ID
	StrategyName   string    `gorm:"size:100;not null"`                        // 策略名称
	StrategyType   string    `gorm:"size:30"`                                  // 策略类型
	StartDate      string    `gorm:"size:20;not null"`                         // 回测开始日期
	EndDate        string    `gorm:"size:20;not null"`                         // 回测结束日期
	InitialCapital float64   `gorm:"type:decimal(18,2);not null"`              // 初始资金
	FinalCapital   float64   `gorm:"type:decimal(18,2)"`                       // 期末资金
	AnnualReturn   float64   `gorm:"type:decimal(10,4)"`                       // 年化收益率(%)
	SharpeRatio    float64   `gorm:"type:decimal(10,4)"`                       // 夏普比率
	MaxDrawdown    float64   `gorm:"type:decimal(10,4)"`                       // 最大回撤(%)
	WinRate        float64   `gorm:"type:decimal(10,4)"`                       // 胜率(%)
	ProfitFactor   float64   `gorm:"type:decimal(10,4)"`                       // 盈亏比
	TotalTrades    int       `gorm:"default:0"`                                // 总交易次数
	Status         string    `gorm:"index;size:20;not null;default:COMPLETED"` // COMPLETED/FAILED/RUNNING
	ErrorMessage   string    `gorm:"type:text"`                                // 错误信息
	ConfigJSON     string    `gorm:"type:text"`                                // 回测配置JSON
	ResultJSON     string    `gorm:"type:text"`                                // 完整结果JSON（含详细交易记录等）
	OperatorID     string    `gorm:"size:50"`                                  // 操作人ID
	OperatorName   string    `gorm:"size:50"`                                  // 操作人名称
	RunTime        time.Time `gorm:"index;not null"`                           // 回测运行时间
	DurationMs     int       `gorm:"default:0"`                                // 回测耗时
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BacktestConfig 回测配置（用于快速查询）
type BacktestConfig struct {
	ID             uint    `gorm:"primaryKey"`
	Name           string  `gorm:"size:100;not null"`      // 配置名称
	StrategyID     string  `gorm:"index;size:50;not null"` // 关联策略ID
	StartDate      string  `gorm:"size:20;not null"`
	EndDate        string  `gorm:"size:20;not null"`
	InitialCapital float64 `gorm:"type:decimal(18,2);not null;default:100000"`
	Benchmark      string  `gorm:"size:20;default:000300.SZ"`        // 基准指数
	Commission     float64 `gorm:"type:decimal(6,4);default:0.0003"` // 佣金率
	Slippage       float64 `gorm:"type:decimal(6,4);default:0.0001"` // 滑点
	ConfigJSON     string  `gorm:"type:text"`                        // 扩展配置JSON
	IsDefault      int     `gorm:"not null;default:0"`               // 是否默认配置
	CreatedBy      string  `gorm:"size:50"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DailyReview 每日复盘报告表（CIO每日收盘后生成的复盘记录）
type DailyReview struct {
	ID               uint    `gorm:"primaryKey"`
	ReviewDate       string  `gorm:"uniqueIndex;size:10;not null"` // YYYY-MM-DD
	PortfolioID      string  `gorm:"index;size:50"`
	TotalAssets      float64 `gorm:"type:decimal(18,2)"`
	TotalReturn      float64 `gorm:"type:decimal(10,4)"`
	DailyPnL         float64 `gorm:"type:decimal(18,2)"`
	DailyReturn      float64 `gorm:"type:decimal(10,4)"`
	Cash             float64 `gorm:"type:decimal(18,2)"`
	MarketValue      float64 `gorm:"type:decimal(18,2)"`
	PositionsCount   int     `gorm:"type:int"`
	TradeCount       int     `gorm:"type:int"`
	TradePnL         float64 `gorm:"type:decimal(18,2)"`
	MarketRegime     string  `gorm:"size:20"`
	MarketConfidence float64 `gorm:"type:decimal(5,4)"`
	TopGainers       string  `gorm:"type:text"` // JSON
	TopLosers        string  `gorm:"type:text"` // JSON
	RiskMetrics      string  `gorm:"type:text"` // JSON
	KeyEvents        string  `gorm:"type:text"` // JSON
	Decisions        string  `gorm:"type:text"` // JSON
	// Quant复盘：持仓执行策略解读（JSON：每只持仓的策略、表现与解读）
	PositionStrategyReview string `gorm:"type:text"`
	// Quant复盘：是否调整策略的建议（JSON：建议与理由）
	StrategyChangeSuggestion string `gorm:"type:text"`
	// 各智能体复盘汇总（JSON：role->[{task,phase,deliverable,summary}]），由当日各Agent任务交付物聚合
	AgentSummaries  string    `gorm:"type:text"`
	Summary         string    `gorm:"type:text"`
	Recommendations string    `gorm:"type:text"`
	Status          string    `gorm:"index;size:20;not null;default:COMPLETED"`
	CreatedAt       time.Time `gorm:"index;not null"`
}

// AgentTaskLog 智能体任务日志表（记录盘前/盘后任务交付物）
type AgentTaskLog struct {
	ID              uint   `gorm:"primaryKey"`
	TaskDate        string `gorm:"index;size:10;not null"`  // YYYY-MM-DD
	TaskPhase       string `gorm:"index;size:20;not null"`  // PRE_MARKET / POST_MARKET
	AgentRole       string `gorm:"index;size:30;not null"`  // PLANNER / QUANT / CIO / RISK / TRADER
	TaskName        string `gorm:"index;size:100;not null"` // 任务名称
	TaskOrder       int    `gorm:"type:int"`                // 执行顺序
	Status          string `gorm:"index;size:20;not null"`  // PENDING / RUNNING / COMPLETED / FAILED
	StartTime       *time.Time
	EndTime         *time.Time
	DurationMs      int64     `gorm:"type:bigint"`
	DeliverableType string    `gorm:"size:50"`   // 交付物类型
	DeliverableName string    `gorm:"size:200"`  // 交付物名称
	DeliverableData string    `gorm:"type:text"` // 交付物JSON
	Summary         string    `gorm:"type:text"` // 任务摘要
	Details         string    `gorm:"type:text"` // 详细信息JSON
	Errors          string    `gorm:"type:text"` // 错误信息
	CreatedAt       time.Time `gorm:"index;not null"`
}

// ClaimRecord 可证伪声明台账（P0）：记录智能体盘前/复盘时对市场的方向主张与信心，
// 供日后用真实结算收益对账，量化"智能体判断准确率"（命中率）并反哺信心值。
type ClaimRecord struct {
	ID           uint    `gorm:"primaryKey"`
	ClaimDate    string  `gorm:"index;size:10;not null"` // YYYY-MM-DD（主张当日）
	DecisionID   string  `gorm:"index;size:64"`
	MarketTag    string  `gorm:"size:30"`            // 六维判势标签，如 强势/结构性震荡/退潮风险
	Direction    string  `gorm:"size:10"`            // bullish / bearish / neutral（相对市场方向主张）
	Statement    string  `gorm:"size:512"`           // 主张陈述（来自决策理由，简述）
	Confidence   float64 `gorm:"type:decimal(5,2)"`  // 信心 0~100
	Verified     bool    `gorm:"index"`              // 是否已用真实收益验证
	Match        bool    `gorm:"default:false"`      // 方向判断是否命中
	ActualReturn float64 `gorm:"type:decimal(10,4)"` // 验证日实际市场收益(%)
	VerifiedAt   time.Time
	CreatedAt    time.Time `gorm:"index;not null"`
}

// AlphaStore 结构化 Alpha 候选库（P1）：沉淀经回测/因子复盘通过的策略与因子签名，
// 记录其适用的市场状态(regime)，供次日盘前按当前 regime 检索注入决策上下文。
type AlphaStore struct {
	ID          uint      `gorm:"primaryKey"`
	Name        string    `gorm:"index;size:100;not null"`
	Source      string    `gorm:"size:30"`       // strategy / factor / llm
	SourceType  string    `gorm:"size:50"`       // 具体策略类型或因子名
	Regime      string    `gorm:"index;size:30"` // 生效市场标签
	SignalDate  string    `gorm:"index;size:10"` // YYYY-MM-DD 沉淀日期
	Metric      string    `gorm:"size:20"`       // backtest_sharpe / factor_quality / …
	MetricValue float64   `gorm:"type:decimal(10,4)"`
	Description string    `gorm:"size:512"`
	CreatedAt   time.Time `gorm:"index;not null"`
}

// PositionSnapshot 每日持仓快照表（每日收盘后记录每只股票的持仓数据）
type PositionSnapshot struct {
	ID               uint    `gorm:"primaryKey"`
	SnapshotDate     string  `gorm:"uniqueIndex:idx_date_symbol;size:10;not null"` // YYYY-MM-DD
	PortfolioID      uint    `gorm:"index;not null"`
	InstrumentID     string  `gorm:"uniqueIndex:idx_date_symbol;size:30;not null"` // 600519.SH
	StockName        string  `gorm:"size:100"`
	Market           string  `gorm:"size:10"`
	Quantity         int     `gorm:"not null"`
	ClosePrice       float64 `gorm:"type:decimal(12,4);not null"`
	PrevClose        float64 `gorm:"type:decimal(12,4)"`
	OpenPrice        float64 `gorm:"type:decimal(12,4)"`
	HighPrice        float64 `gorm:"type:decimal(12,4)"`
	LowPrice         float64 `gorm:"type:decimal(12,4)"`
	MarketValue      float64 `gorm:"type:decimal(18,2);not null"`
	UnrealizedPnL    float64 `gorm:"type:decimal(18,2)"`
	UnrealizedReturn float64 `gorm:"type:decimal(10,6)"`
	DailyPnL         float64 `gorm:"type:decimal(18,2)"` // 当日盈亏 = (收盘价 - 昨收价) × 持仓量
	DailyReturn      float64 `gorm:"type:decimal(10,6)"` // 当日收益率
	Weight           float64 `gorm:"type:decimal(8,6)"`
	CreatedAt        time.Time
}

// PortfolioDailyStat 投资组合每日统计表（每日收盘后记录组合整体数据）
type PortfolioDailyStat struct {
	ID               uint    `gorm:"primaryKey"`
	StatDate         string  `gorm:"uniqueIndex;size:10;not null"` // YYYY-MM-DD
	PortfolioID      uint    `gorm:"index;not null"`
	TotalAssets      float64 `gorm:"type:decimal(18,2);not null"`
	TotalCapital     float64 `gorm:"type:decimal(18,2);not null"`
	Cash             float64 `gorm:"type:decimal(18,2);not null"`
	MarketValue      float64 `gorm:"type:decimal(18,2);not null"`
	TotalPnL         float64 `gorm:"type:decimal(18,2)"` // 累计盈亏
	TotalReturn      float64 `gorm:"type:decimal(10,6)"` // 累计收益率
	DailyPnL         float64 `gorm:"type:decimal(18,2)"` // 当日盈亏
	DailyReturn      float64 `gorm:"type:decimal(10,6)"` // 当日收益率
	PositionsCount   int     `gorm:"type:int"`
	TotalDailyVolume float64 `gorm:"type:decimal(18,2)"` // 当日成交金额
	TradeCount       int     `gorm:"type:int"`
	CreatedAt        time.Time
}

// ==================== 默认数据初始化 ====================

// initDefaultData 初始化默认配置数据
func initDefaultData(db *gorm.DB) {
	// 初始化系统配置
	var configCount int64
	db.Model(&SystemConfig{}).Count(&configCount)
	if configCount == 0 {
		featuresJSON := `[
			"实时行情监控",
			"智能选股引擎",
			"AI投资规划",
			"组合优化",
			"风险评估",
			"回测验证",
			"AI团队协作",
			"每日复盘报告"
		]`
		aboutText := `QuantBot AI 是一款由大模型驱动的量化机器人。

系统采用多Agent协作架构，包含Planner、Quant、CIO、Risk、Trader五大智能体，覆盖盘前分析、盘中监控、盘后复盘全流程。

技术栈：Go + React + SQLite + DuckDB`

		db.Create(&SystemConfig{
			AppVersion:   version.Version,
			AppName:      "QuantBot AI",
			AboutText:    aboutText,
			FeaturesJSON: featuresJSON,
		})
	}

	// 清理旧的关于页面内容（版本对比、多因子评分体系、因子健康度分析），兼容历史数据库
	var cfg SystemConfig
	if err := db.Order("id ASC").First(&cfg).Error; err == nil {
		changed := false
		about := cfg.AboutText
		if idx := strings.Index(about, "版本对比"); idx >= 0 {
			// 去掉「版本对比」段落，保留技术栈等后续内容
			if techIdx := strings.Index(about[idx:], "技术栈"); techIdx >= 0 {
				about = strings.TrimRight(about[:idx], " \n\r") + "\n\n" + about[idx+techIdx:]
			} else {
				about = strings.TrimRight(about[:idx], " \n\r")
			}
			changed = true
		}
		// 产品定位描述更新（专业的A股智能投资分析系统 -> 由大模型驱动的量化机器人）
		if strings.Contains(about, "专业的A股智能投资分析系统") {
			about = strings.ReplaceAll(about, "专业的A股智能投资分析系统", "由大模型驱动的量化机器人")
			changed = true
		}
		featsJSON := cfg.FeaturesJSON
		var feats []string
		if json.Unmarshal([]byte(cfg.FeaturesJSON), &feats) == nil {
			cleaned := feats[:0]
			for _, f := range feats {
				if f == "多因子评分体系" || f == "因子健康度分析" {
					changed = true
					continue
				}
				cleaned = append(cleaned, f)
			}
			if len(cleaned) != len(feats) {
				b, _ := json.Marshal(cleaned)
				featsJSON = string(b)
			}
		}
		if changed {
			db.Model(&SystemConfig{}).Where("id = ?", cfg.ID).Update("about_text", about)
			if featsJSON != cfg.FeaturesJSON {
				db.Model(&SystemConfig{}).Where("id = ?", cfg.ID).Update("features_json", featsJSON)
			}
			log.Printf("[InitSQLite] 已更新关于页面内容（版本对比清理/产品定位描述更新）")
		}
	}

	// 初始化默认监控股票（从原硬编码列表迁移至数据库）
	var watchCount int64
	db.Model(&WatchStock{}).Count(&watchCount)
	if watchCount == 0 {
		loader := GetDictLoader()
		defaultStocks := []WatchStock{
			// 消费/医药
			{Code: "600519", Sector: "消费/医药", SortOrder: 1},
			{Code: "600276", Sector: "消费/医药", SortOrder: 2},
			{Code: "000858", Sector: "消费/医药", SortOrder: 3},
			{Code: "600887", Sector: "消费/医药", SortOrder: 4},
			{Code: "000538", Sector: "消费/医药", SortOrder: 5},
			{Code: "300760", Sector: "消费/医药", SortOrder: 6},
			{Code: "603288", Sector: "消费/医药", SortOrder: 7},
			{Code: "000651", Sector: "消费/医药", SortOrder: 8},
			{Code: "600690", Sector: "消费/医药", SortOrder: 9},
			{Code: "000333", Sector: "消费/医药", SortOrder: 10},
			// 科技/新能源
			{Code: "002594", Sector: "科技/新能源", SortOrder: 11},
			{Code: "300750", Sector: "科技/新能源", SortOrder: 12},
			{Code: "601012", Sector: "科技/新能源", SortOrder: 13},
			{Code: "002475", Sector: "科技/新能源", SortOrder: 14},
			{Code: "300059", Sector: "科技/新能源", SortOrder: 15},
			{Code: "688981", Sector: "科技/新能源", SortOrder: 16},
			{Code: "300124", Sector: "科技/新能源", SortOrder: 17},
			{Code: "600438", Sector: "科技/新能源", SortOrder: 18},
			// 金融/地产
			{Code: "600036", Sector: "金融/地产", SortOrder: 19},
			{Code: "601318", Sector: "金融/地产", SortOrder: 20},
			{Code: "601398", Sector: "金融/地产", SortOrder: 21},
			{Code: "601939", Sector: "金融/地产", SortOrder: 22},
			{Code: "600000", Sector: "金融/地产", SortOrder: 23},
			{Code: "600030", Sector: "金融/地产", SortOrder: 24},
			{Code: "000001", Sector: "金融/地产", SortOrder: 25},
			{Code: "601166", Sector: "金融/地产", SortOrder: 26},
			// 周期/资源
			{Code: "601899", Sector: "周期/资源", SortOrder: 27},
			{Code: "600028", Sector: "周期/资源", SortOrder: 28},
			{Code: "601857", Sector: "周期/资源", SortOrder: 29},
			{Code: "601628", Sector: "周期/资源", SortOrder: 30},
			{Code: "600585", Sector: "周期/资源", SortOrder: 31},
			{Code: "000725", Sector: "周期/资源", SortOrder: 32},
			{Code: "600050", Sector: "周期/资源", SortOrder: 33},
			{Code: "601988", Sector: "周期/资源", SortOrder: 34},
			// 制造/工业
			{Code: "000100", Sector: "制造/工业", SortOrder: 35},
			{Code: "600660", Sector: "制造/工业", SortOrder: 36},
			{Code: "002415", Sector: "制造/工业", SortOrder: 37},
			{Code: "600745", Sector: "制造/工业", SortOrder: 38},
			{Code: "002371", Sector: "制造/工业", SortOrder: 39},
			{Code: "603501", Sector: "制造/工业", SortOrder: 40},
			// 国防军工
			{Code: "600760", Sector: "国防军工", SortOrder: 41},
			{Code: "000768", Sector: "国防军工", SortOrder: 42},
			{Code: "002013", Sector: "国防军工", SortOrder: 43},
			// 公用事业
			{Code: "600900", Sector: "公用事业", SortOrder: 44},
			{Code: "600886", Sector: "公用事业", SortOrder: 45},
			{Code: "601985", Sector: "公用事业", SortOrder: 46},
		}
		for _, s := range defaultStocks {
			name := loader.GetStockName(s.Code)
			market := detectMarket(s.Code)
			db.Create(&WatchStock{
				Code:      s.Code,
				Name:      name,
				Market:    market,
				Sector:    s.Sector,
				SortOrder: s.SortOrder,
				IsActive:  1,
			})
		}
	}
}

// GetActiveMarketIndices 获取当前活跃的市场指数列表
func GetActiveMarketIndices(db *gorm.DB) []MarketIndex {
	var indices []MarketIndex
	db.Where("is_active = ?", 1).Order("sort_order").Find(&indices)
	return indices
}

// GetDefaultWatchStocks 获取当前活跃的监控股票列表（从数据库读取，替代硬编码）
func GetDefaultWatchStocks(db *gorm.DB) []WatchStock {
	var stocks []WatchStock
	db.Where("is_active = ?", 1).Order("sort_order").Find(&stocks)
	return stocks
}

// GetWatchStockNameMap 获取代码→名称映射（用于 Mock 数据生成时查找名称）
func GetWatchStockNameMap(db *gorm.DB) map[string]string {
	stocks := GetDefaultWatchStocks(db)
	m := make(map[string]string, len(stocks))
	for _, s := range stocks {
		m[s.Code] = s.Name
	}
	return m
}

// GetWatchStockCodeMap 获取代码→WatchStock 完整对象映射
func GetWatchStockCodeMap(db *gorm.DB) map[string]WatchStock {
	stocks := GetDefaultWatchStocks(db)
	m := make(map[string]WatchStock, len(stocks))
	for _, s := range stocks {
		m[s.Code] = s
	}
	return m
}

// GetSystemConfig 获取系统配置
func GetSystemConfig(db *gorm.DB) SystemConfig {
	var config SystemConfig
	if err := db.First(&config).Error; err != nil {
		return SystemConfig{
			AppVersion: version.Version,
			AppName:    "QuantBot AI",
		}
	}
	return config
}
