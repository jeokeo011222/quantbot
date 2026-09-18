//go:build !windows

package main

import "log"

// SingleInstance 非Windows平台的单实例检查（简化实现）
func SingleInstance() (bool, uintptr) {
	log.Println("[SingleInstance] Non-Windows platform, skipping mutex check")
	return true, 0
}

// ReleaseMutex 非Windows平台的释放互斥量
func ReleaseMutex(handle uintptr) {
	// 非Windows平台无需释放
}

// showAlreadyRunningDialog 非Windows平台的对话框
func showAlreadyRunningDialog() {
	log.Println("[SingleInstance] Another instance is already running, exiting...")
}
