package backtest

import "testing"

// TestMLModelDefaultEnsembleMethod 验证 ml_model 策略默认集成方法为概率平均，
// 且 DB 配置（仅显式 ensemble_method）能正确覆盖策略实例字段。
func TestMLModelDefaultEnsembleMethod(t *testing.T) {
	st := GetStrategyByType("ml_model")
	ml, ok := st.(*MlModelStrategy)
	if !ok {
		t.Fatalf("GetStrategyByType(ml_model) 类型错误: %T", st)
	}
	if ml.EnsembleMethod != "probability" {
		t.Errorf("代码默认 EnsembleMethod = %q，期望 probability", ml.EnsembleMethod)
	}
	if len(ml.ModelNames) != 5 {
		t.Errorf("代码默认 ModelNames = %d 个，期望 5", len(ml.ModelNames))
	}

	// 模拟 DB 配置路径：BuildStrategyFromConfig 只覆盖 config 里出现的字段
	cfg := `{"capital":100000,"max_position":10,"stop_loss":0.08,"take_profit":0.25,"ensemble_method":"probability"}`
	built := BuildStrategyFromConfig("ml_model", ParseConfigJSON(cfg))
	bml, ok := built.(*MlModelStrategy)
	if !ok {
		t.Fatalf("BuildStrategyFromConfig(ml_model) 类型错误: %T", built)
	}
	if bml.EnsembleMethod != "probability" {
		t.Errorf("DB 配置 EnsembleMethod = %q，期望 probability", bml.EnsembleMethod)
	}
	if len(bml.ModelNames) != 5 {
		t.Errorf("DB 未显式配置 model_names 时应沿用代码默认 5 模型，实际 %d", len(bml.ModelNames))
	}
}
