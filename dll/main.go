// Package main 构建为 -buildmode=c-shared 的 agent.dll。
//
// 决策脑（agents/llm/planner/sixdim/intelligence 及工具 schema/权限逻辑）全部编译进此 DLL，
// 仅对外暴露粗粒度 JSON C ABI。宿主程序通过 LoadLibrary 加载并调用导出函数。
//
// 跨进程边界只允许传递 *C.char（JSON 字符串），严禁传递含指针的 Go 内存，规避双运行时 checkptr 崩溃。
package main

import "C"

// main 供 -buildmode=c-shared 使用；空实现，真正入口在 export.go 的 //export 函数。
func main() {
	// do nothing: this is a shared library.
}