# guardrails 模式构建与检查目标
#
# 用法：
#   make guard-vet          # 跑 guardvet 规则（扫描多轮循环是否注入护栏）
#   make guard-verbose      # verbose 模式
#   make guard-build        # 单独编译 guardvet 二进制
#   make vet                # Go 标准 vet + guardvet（推荐 CI 目标）
#   make pattern-template   # 查看 guardrails 跨文件模式模板（打印文件）

GO ?= go
GUARDVET_DIR := ./cmd/guardvet
GUARDVET_BIN := ./.bin/guardvet

.PHONY: guard-vet guard-verbose guard-build vet pattern-template guard-clean

# 快速检查：跑 guardvet（静态扫描，无依赖）
guard-vet: guard-build
	@echo "===  guardvet: guardrails 护栏检查 ==="
	@$(GUARDVET_BIN) ./internal/... || true
	@echo ""
	@echo "done. (exit != 0 表示发现问题；Exit 0 表示 OK)"

# verbose 模式：打印 AST 扫描痕迹
guard-verbose: guard-build
	@$(GUARDVET_BIN) -verbose ./internal/... || true

# 编译 guardvet 到本地 .bin/ 目录
guard-build:
	@mkdir -p .bin
	@$(GO) build -o $(GUARDVET_BIN) $(GUARDVET_DIR)

# Go 标准 vet + guardvet（推荐 CI 目标）
vet: guard-vet
	@echo "===  go vet ./... ==="
	@$(GO) vet ./... || true

# 打印跨文件模式模板
pattern-template:
	@echo "=== guardrails 跨文件模式模板（见 internal/guardrails/pattern_template.go） ==="
	@cat internal/guardrails/pattern_template.go

guard-clean:
	@rm -f $(GUARDVET_BIN)
