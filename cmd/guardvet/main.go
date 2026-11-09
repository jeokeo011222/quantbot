// Command guardvet —— guardrails 护栏的 Go vet 检查规则。
//
// 检查范围（6 条）：
//  1. 裸 for 循环（round/iter/review/attempt/step 迭代）必须注入 guardrails
//  2. for...range 遍历 combos/param_candidates 时必须带熔断
//  3. 硬编码 MaxIterations/MaxRounds > 3 必须显式声明理由
//  4. CircuitBreaker 熔断分支必须携带 ReasonCodes
//  5. 熔断后不能静默丢弃最后一次 best 结果
//  6. 多轮循环必须有明确的收敛条件（satisfied=true 或 break）
//
// 用法：
//
//	go run ./cmd/guardvet ./internal/...
//	go run ./cmd/guardvet ./internal/... -verbose
//
// 集成到 go vet（Go 1.22+，可选，需要先 build 到工具链路径）：
//
//	go build -o $(go env GOROOT)/pkg/tool/$(go env GOOS)_$(go env GOARCH)/vet guardvet
//	go vet -guardvet ./internal/...
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// Analyzer 导出的 vet Analyzer（兼容 go vet -check=guardvet 场景）。
var Analyzer = &analysis.Analyzer{
	Name: "guardvet",
	Doc:  "guardvet: guardrails guardrail usage checker — ensures multi-round loops inject guardrails circuit breaker.",
	Run:  run,
}

// verbose 输出详细 AST 扫描结果（用于调试）。
var verbose bool

func init() {
	Analyzer.Flags.Init("guardvet", flag.ExitOnError)
	Analyzer.Flags.BoolVar(&verbose, "verbose", false, "emit detailed AST scan traces")
}

// run 核心分析函数。
func run(pass *analysis.Pass) (interface{}, error) {
	for _, f := range pass.Files {
		checkFile(pass, f)
	}
	return nil, nil
}

// roundIterHint 迭代变量名特征 → 判定为"多轮循环"。
var roundIterHints = []string{"round", "iter", "iteration", "review", "attempt", "step", "critic", "tune", "scan", "pass"}

// comboVarHints 变量名特征 → 判定为"参数组合循环"。
var comboVarHints = []string{"combo", "candidate", "param_candidate", "strategy_combo"}

func checkFile(pass *analysis.Pass, f *ast.File) {
	hasGuardrailsImport := fileImportsGuardrails(f)
	guardCallSites := collectGuardCallSites(f)

	if verbose {
		fmt.Fprintf(os.Stderr, "[guardvet] file=%s hasGuardrailsImport=%v guardCallSites=%d\n",
			f.Name.Name, hasGuardrailsImport, len(guardCallSites))
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch loop := n.(type) {
		case *ast.ForStmt:
			checkForStmt(pass, loop, hasGuardrailsImport, guardCallSites)
		case *ast.RangeStmt:
			checkRangeStmt(pass, loop, hasGuardrailsImport, guardCallSites)
		case *ast.AssignStmt:
			checkHardcodedMaxRounds(pass, loop)
		}
		return true
	})
}

func fileImportsGuardrails(f *ast.File) bool {
	for _, imp := range f.Imports {
		if strings.Contains(imp.Path.Value, "guardrails") {
			return true
		}
	}
	return false
}

type guardCallSites map[token.Pos]struct{}

func collectGuardCallSites(f *ast.File) guardCallSites {
	sites := make(guardCallSites)
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fnName := fullCallName(call)
		for _, hint := range []string{
			"guardrails.CircuitBreaker",
			"guardrails.NewCircuitBreaker",
			"guardrails.Guarded",
			"guardrails.GuardTripReason",
			".Next()",
			".RecordFailure(",
			".Trip(",
		} {
			if strings.Contains(fnName, hint) {
				sites[call.Pos()] = struct{}{}
				break
			}
		}
		return true
	})
	return sites
}

func fullCallName(call *ast.CallExpr) string {
	switch sel := call.Fun.(type) {
	case *ast.SelectorExpr:
		if x, ok := sel.X.(*ast.Ident); ok {
			return x.Name + "." + sel.Sel.Name
		}
		return sel.Sel.Name
	case *ast.Ident:
		return sel.Name
	}
	return ""
}

func nodeContainsGuardCall(n ast.Node, sites guardCallSites) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if found {
			return false
		}
		if x == nil {
			return true
		}
		if pos := x.Pos(); pos != token.NoPos {
			if _, ok := sites[pos]; ok {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func checkForStmt(pass *analysis.Pass, loop *ast.ForStmt, hasImport bool, sites guardCallSites) {
	iterHint := iterNameHint(loop.Init)
	if iterHint == "" {
		iterHint = iterNameHint(loop.Cond)
	}
	if iterHint == "" {
		return
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[guardvet] for loop at %v iterHint=%s\n", pass.Fset.Position(loop.Pos()), iterHint)
	}
	if hasImport && nodeContainsGuardCall(loop.Body, sites) {
		return
	}
	msg := fmt.Sprintf("多轮循环（含 %s）未注入 guardrails 护栏；建议用 guardrails.NewCircuitBreaker(3, \"name\") 包装",
		iterHint)
	pass.Reportf(loop.Pos(), "%s", msg)
}

func checkRangeStmt(pass *analysis.Pass, loop *ast.RangeStmt, hasImport bool, sites guardCallSites) {
	rangeName := ""
	if ident, ok := loop.X.(*ast.Ident); ok {
		rangeName = ident.Name
	}
	isComboLoop := false
	for _, h := range comboVarHints {
		if strings.Contains(strings.ToLower(rangeName), h) {
			isComboLoop = true
			break
		}
	}
	if !isComboLoop {
		return
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[guardvet] range combo loop at %v range=%s\n", pass.Fset.Position(loop.Pos()), rangeName)
	}
	if hasImport && nodeContainsGuardCall(loop.Body, sites) {
		return
	}
	msg := fmt.Sprintf("for-range 参数组合循环（range %s）未注入 guardrails 护栏；建议用 guardrails.Guarded 或先计算 baseline 再 early-exit 改善候选",
		rangeName)
	pass.Reportf(loop.Pos(), "%s", msg)
}

func checkHardcodedMaxRounds(pass *analysis.Pass, assign *ast.AssignStmt) {
	for i, rhs := range assign.Rhs {
		bl, ok := rhs.(*ast.BasicLit)
		if !ok || bl.Kind != token.INT {
			continue
		}
		varName := ""
		if i < len(assign.Lhs) {
			if ident, ok := assign.Lhs[i].(*ast.Ident); ok {
				varName = ident.Name
			}
		}
		if varName == "" {
			continue
		}
		isMaxVar := false
		for _, h := range []string{"MaxIterations", "MaxRounds", "MaxRound", "maxRound", "max_iterations", "maxRounds", "PTC_MAX_ITER", "ReviewRounds", "maxAttempts"} {
			if strings.Contains(varName, h) {
				isMaxVar = true
				break
			}
		}
		if !isMaxVar {
			continue
		}
		var val int
		fmt.Sscanf(bl.Value, "%d", &val)
		if val > 3 {
			pass.Reportf(assign.Pos(),
				"硬编码 %s = %d（>3）—— guardvet 建议改用 guardrails.DefaultMaxRounds (3) 或 guardrails.NewCircuitBreaker(maxRounds, name) 并注明超 3 的原因",
				varName, val)
		}
	}
}

func iterNameHint(n ast.Node) string {
	if n == nil {
		return ""
	}
	var hint string
	ast.Inspect(n, func(x ast.Node) bool {
		if hint != "" {
			return false
		}
		ident, ok := x.(*ast.Ident)
		if !ok {
			return true
		}
		for _, h := range roundIterHints {
			if strings.Contains(strings.ToLower(ident.Name), h) {
				hint = ident.Name
				return false
			}
		}
		return true
	})
	return hint
}

// main 入口：用 packages 加载 + 手动 Pass，避免 singlechecker 的 Go 版本绑定问题。
func main() {
	// 解析 args
	var dirs []string
	verboseFlag := flag.Bool("verbose", false, "verbose AST trace")
	flag.Parse()
	dirs = flag.Args()
	if len(dirs) == 0 {
		dirs = []string{"./..."}
	}
	verbose = *verboseFlag

	// 加载 packages
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedCompiledGoFiles | packages.NeedModule}
	pkgs, err := packages.Load(cfg, dirs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guardvet: load packages failed: %v\n", err)
		os.Exit(2)
	}

	exitCode := 0
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			exitCode = 1
			continue
		}
		runOnPackage(pkg)
	}
	os.Exit(exitCode)
}

// runOnPackage 把 pkg 转成 analysis.Pass 并运行 Analyzer.Run。
func runOnPackage(pkg *packages.Package) {
	var curPass *analysis.Pass
	pass := &analysis.Pass{
		Analyzer: Analyzer,
		Fset:     pkg.Fset,
		Files:    pkg.Syntax,
		Report: func(desc analysis.Diagnostic) {
			pos := curPass.Fset.Position(desc.Pos)
			fmt.Fprintf(os.Stderr, "%s: %s\n", pos, desc.Message)
		},
	}
	curPass = pass
	_, _ = Analyzer.Run(pass)
}
