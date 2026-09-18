package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"gorm.io/gorm"
)

// ==================== record_alpha（结构化 Alpha 候选沉淀，P1） ====================

// RecordAlphaTool 将经回测/因子复盘验证通过的策略或因子签名沉淀到 AlphaStore，
// 并标注其适用的市场状态(regime)。供日后盘前按当前 regime 检索复用。
type RecordAlphaTool struct {
	db *data.SQLiteManager
}

func NewRecordAlphaTool(m *data.SQLiteManager) *RecordAlphaTool {
	return &RecordAlphaTool{db: m}
}

func (t *RecordAlphaTool) Name() string { return "record_alpha" }

func (t *RecordAlphaTool) Description() string {
	return "把经回测/因子复盘验证通过的一个 alpha(策略或因子)签名写入 AlphaStore，需提供名称、来源(strategy/factor/llm)、适用市场标签(regime对齐六维判势标签)、量化指标(如backtest_sharpe)与说明。供后续盘前按当前市场状态检索复用"
}

func (t *RecordAlphaTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":         map[string]string{"type": "string", "description": "alpha 名称"},
					"source":       map[string]string{"type": "string", "description": "来源: strategy / factor / llm"},
					"source_type":  map[string]string{"type": "string", "description": "具体策略类型或因子名"},
					"regime":       map[string]string{"type": "string", "description": "适用的市场标签，与六维判势一致，如：强势/结构性震荡/退潮风险"},
					"metric":       map[string]string{"type": "string", "description": "指标名，如 backtest_sharpe / factor_quality"},
					"metric_value": map[string]interface{}{"type": "number", "description": "指标值"},
					"description":  map[string]string{"type": "string", "description": "说明/机制"},
				},
				"required": []string{"name", "source", "regime", "metric"},
			},
		},
	}
}

func (t *RecordAlphaTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.db == nil {
		return map[string]interface{}{"status": "unavailable", "message": "数据库未初始化"}, nil
	}
	name, _ := args["name"].(string)
	source, _ := args["source"].(string)
	sourceType, _ := args["source_type"].(string)
	regime, _ := args["regime"].(string)
	metric, _ := args["metric"].(string)
	desc, _ := args["description"].(string)
	name = strings.TrimSpace(name)
	if name == "" || regime == "" {
		return map[string]interface{}{"status": "error", "message": "name/regime 必填"}, nil
	}
	var mv float64
	if v, ok := args["metric_value"].(float64); ok {
		mv = v
	}
	rec := data.AlphaStore{
		Name:        name,
		Source:      source,
		SourceType:  sourceType,
		Regime:      regime,
		SignalDate:  time.Now().Format("2006-01-02"),
		Metric:      metric,
		MetricValue: mv,
		Description: strings.TrimSpace(desc),
		CreatedAt:   time.Now(),
	}
	if err := t.db.GetDB().Create(&rec).Error; err != nil {
		return map[string]interface{}{"status": "error", "message": fmt.Sprintf("写入 AlphaStore 失败: %v", err)}, nil
	}
	return map[string]interface{}{"status": "ok", "id": rec.ID, "message": "已沉淀 alpha 候选", "source": "AlphaStore 表"}, nil
}

// ==================== get_alpha_by_regime（按市场状态检索 alpha，P1） ====================

// GetAlphaByRegimeTool 按六维判势标签(regime)从 AlphaStore 检索历史验证通过的 alpha 候选，
// 供 CIO/Quant 盘前在相似市场状态时复用。无精确匹配时回退到近 30 天全部候选。
type GetAlphaByRegimeTool struct {
	db *data.SQLiteManager
}

func NewGetAlphaByRegimeTool(m *data.SQLiteManager) *GetAlphaByRegimeTool {
	return &GetAlphaByRegimeTool{db: m}
}

func (t *GetAlphaByRegimeTool) Name() string { return "get_alpha_by_regime" }

func (t *GetAlphaByRegimeTool) Description() string {
	return "按六维市场标签(regime，如：强势/结构性震荡/退潮风险)从AlphaStore检索已验证通过的alpha候选(策略/因子签名及指标)。盘前据此在相似市场状态下复用历史有效alpha"
}

func (t *GetAlphaByRegimeTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"regime":   map[string]string{"type": "string", "description": "当前市场标签(六维判势)"},
					"intraday": map[string]interface{}{"type": "boolean", "description": "是否额外返回近期全部候选，可选"},
				},
			},
		},
	}
}

func (t *GetAlphaByRegimeTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.db == nil {
		return map[string]interface{}{"status": "unavailable", "message": "数据库未初始化"}, nil
	}
	regime, _ := args["regime"].(string)
	regime = strings.TrimSpace(regime)
	db := t.db.GetDB()

	get := func(q *gorm.DB, limit int) ([]data.AlphaStore, error) {
		var rows []data.AlphaStore
		if err := q.Order("created_at desc, metric_value desc").Limit(limit).Find(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}

	var matched []data.AlphaStore
	var recent []data.AlphaStore
	if regime != "" {
		if m, err := get(db.Where("regime = ?", regime), 20); err == nil {
			matched = m
		}
	}
	// 近30天全部候选（供参考）
	since := time.Now().AddDate(0, 0, -30)
	if r, err := get(db.Where("created_at >= ?", since), 20); err == nil {
		recent = r
	}

	type outAlpha struct {
		Name        string  `json:"name"`
		Source      string  `json:"source"`
		SourceType  string  `json:"source_type"`
		Regime      string  `json:"regime"`
		SignalDate  string  `json:"signal_date"`
		Metric      string  `json:"metric"`
		MetricValue float64 `json:"metric_value"`
		Description string  `json:"description"`
	}
	mapper := func(rows []data.AlphaStore) []outAlpha {
		out := make([]outAlpha, 0, len(rows))
		for _, r := range rows {
			out = append(out, outAlpha{r.Name, r.Source, r.SourceType, r.Regime, r.SignalDate, r.Metric, r.MetricValue, r.Description})
		}
		return out
	}

	return map[string]interface{}{
		"status":         "ok",
		"regime_matched": mapper(matched),
		"recent_30d":     mapper(recent),
		"source":         "AlphaStore 表",
	}, nil
}

// ==================== get_alpha_top（Alpha 候选排行，P1） ====================

// GetAlphaTopTool 返回 AlphaStore 中质量最高的候选排行（按指标值降序），供复盘/构建股票池参考。
type GetAlphaTopTool struct {
	db *data.SQLiteManager
}

func NewGetAlphaTopTool(m *data.SQLiteManager) *GetAlphaTopTool {
	return &GetAlphaTopTool{db: m}
}

func (t *GetAlphaTopTool) Name() string { return "get_alpha_top" }

func (t *GetAlphaTopTool) Description() string {
	return "返回AlphaStore中指标值最高的候选Alpha排行（按metric_value降序），供CIO/Planner在构建股票池、复核策略时参考历史最有效alpha"
}

func (t *GetAlphaTopTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"limit": map[string]interface{}{"type": "integer", "description": "返回条数，默认10"},
			}},
		},
	}
}

func (t *GetAlphaTopTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.db == nil {
		return map[string]interface{}{"status": "unavailable", "message": "数据库未初始化"}, nil
	}
	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	var rows []data.AlphaStore
	if err := t.db.GetDB().Order("metric_value desc").Limit(limit).Find(&rows).Error; err != nil {
		return map[string]interface{}{"status": "error", "message": fmt.Sprintf("查询失败: %v", err)}, nil
	}
	items := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]interface{}{
			"name": r.Name, "source": r.Source, "source_type": r.SourceType,
			"regime": r.Regime, "metric": r.Metric, "metric_value": r.MetricValue,
			"signal_date": r.SignalDate, "description": r.Description,
		})
	}
	return map[string]interface{}{"status": "ok", "alphas": items, "source": "AlphaStore 表"}, nil
}
