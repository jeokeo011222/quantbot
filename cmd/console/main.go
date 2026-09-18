package main

import (
	"context"
	"log"
	"strings"

	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/harness"
)

func maskAPIKey(key string) string {
	if len(key) <= 10 {
		return "****"
	}
	return key[:6] + strings.Repeat("*", len(key)-10) + key[len(key)-4:]
}

func main() {
	log.Println("========================================")
	log.Println("QuantBot - AI Quantitative Trading System")
	log.Println("Version: 0.1.0 (Console Mode)")
	log.Println("========================================")

	// Step 1: Initialize configuration
	log.Println("[Init] Loading configuration...")
	configManager, err := config.NewConfigManager()
	if err != nil {
		log.Fatalf("Failed to init config: %v", err)
	}
	cfg := configManager.GetConfig()
	log.Printf("[Init] Config loaded: market=%s, mode=%s", cfg.Market, cfg.TradingMode)
	log.Printf("[Init] AI Config: provider=%s, model=%s, baseURL=%s, apiKey=%s",
		cfg.AIProvider, cfg.AIModel, cfg.AIBaseURL, maskAPIKey(cfg.AIAPIKey))

	// Step 2: Initialize databases
	log.Println("[Init] Initializing SQLite...")
	sqliteManager, err := data.NewSQLiteManager()
	if err != nil {
		log.Fatalf("Failed to init SQLite: %v", err)
	}
	log.Println("[Init] SQLite initialized")

	log.Println("[Init] Initializing DuckDB...")
	duckdbManager, err := data.NewDuckDBManager()
	if err != nil {
		log.Fatalf("Failed to init DuckDB: %v", err)
	}
	log.Println("[Init] DuckDB initialized")

	// Create Parquet views
	duckdbManager.CreateAllViews()

	// Step 3: Initialize QuantHarness
	log.Println("[Init] Initializing QuantHarness...")
	app, err := harness.NewQuantHarness()
	if err != nil {
		log.Fatalf("Failed to init QuantHarness: %v", err)
	}
	log.Println("[Init] QuantHarness initialized")

	log.Println("========================================")
	log.Println("System ready!")
	log.Printf("Config: market=%s, mode=%s", cfg.Market, cfg.TradingMode)
	log.Println("========================================")

	// Step 4: Run daily cycle
	log.Println("[Demo] Running daily analysis cycle...")
	ctx := context.Background()

	result, err := app.RunDailyCycle(ctx)
	if err != nil {
		log.Printf("[Demo] Daily cycle failed: %v", err)
	} else {
		log.Printf("[Demo] Daily cycle completed:")
		log.Printf("  - Date: %s", result.Date)
		log.Printf("  - Duration: %dms", result.EndTime.Sub(result.StartTime).Milliseconds())
		log.Printf("  - Errors: %d", len(result.Errors))

		if result.MarketView != nil {
			log.Printf("  - Market View: %v", result.MarketView)
		}
		if result.AlphaSignals != nil {
			log.Printf("  - Alpha Signals: %v", result.AlphaSignals)
		}
		if result.RiskAssessment != nil {
			log.Printf("  - Risk Assessment: %v", result.RiskAssessment)
		}
		if result.PortfolioPlan != nil {
			log.Printf("  - Portfolio Plan: %v", result.PortfolioPlan)
		}
		if result.Decision != nil {
			log.Printf("  - Decision: %v", result.Decision)
		}
	}

	// Clean up
	app.Close()
	sqliteManager.Close()
	duckdbManager.Close()

	log.Println("[Done] System shut down cleanly")
}
