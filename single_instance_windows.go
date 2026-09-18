//go:build windows

package main

import (
	"fmt"
	"log"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procReleaseMutex = kernel32.NewProc("ReleaseMutex")
	procCloseHandle  = kernel32.NewProc("CloseHandle")
	procGetLastError = kernel32.NewProc("GetLastError")
)

const (
	mutexName          = "QuantBot_SingleInstance_Mutex_v5"
	errorAlreadyExists = 183 // ERROR_ALREADY_EXISTS
)

// SingleInstance 检查是否为首个实例
// 返回: (是否是首个实例, 互斥量句柄)
func SingleInstance() (bool, uintptr) {
	mutexNamePtr, err := syscall.UTF16PtrFromString(mutexName)
	if err != nil {
		log.Printf("[SingleInstance] Failed to convert mutex name: %v", err)
		return true, 0
	}

	log.Printf("[SingleInstance] Attempting to create mutex: %s", mutexName)

	handle, _, _ := procCreateMutexW.Call(
		0,                                     // lpMutexAttributes
		0,                                     // bInitialOwner (FALSE = not initially owned)
		uintptr(unsafe.Pointer(mutexNamePtr)), // lpName
	)

	// Get the last error to check if mutex already exists
	lastErr, _, _ := procGetLastError.Call()

	log.Printf("[SingleInstance] CreateMutex result: handle=%d, err=%s", handle, formatWindowsError(lastErr))

	if handle == 0 {
		log.Printf("[SingleInstance] Failed to create mutex, assuming first instance")
		return true, 0
	}

	// Check if the mutex already existed
	if lastErr == errorAlreadyExists {
		log.Printf("[SingleInstance] Another instance is already running!")
		// Close the handle since we're not the first instance
		procCloseHandle.Call(handle)
		return false, 0
	}

	log.Printf("[SingleInstance] First instance, mutex created successfully")
	return true, handle
}

// ReleaseMutex 释放互斥量
func ReleaseMutex(handle uintptr) {
	if handle == 0 {
		return
	}
	procReleaseMutex.Call(handle)
	procCloseHandle.Call(handle)
}

// showAlreadyRunningDialog 显示"程序已在运行中"对话框
func showAlreadyRunningDialog() {
	// 简单地使用系统对话框提示
	// 使用 MessageBoxW
	user32 := syscall.NewLazyDLL("user32.dll")
	procMessageBoxW := user32.NewProc("MessageBoxW")

	caption, _ := syscall.UTF16PtrFromString("QuantBot AI量化机器人")
	message, _ := syscall.UTF16PtrFromString("错误：程序已在运行中，请勿重复启动。\n\n请关闭已打开的 QuantBot 窗口后再试。")

	procMessageBoxW.Call(
		0,                                // hWnd
		uintptr(unsafe.Pointer(message)), // lpText
		uintptr(unsafe.Pointer(caption)), // lpCaption
		0x10,                             // uType (MB_ICONERROR)
	)
}

// formatWindowsError 格式化Windows错误码
func formatWindowsError(errCode uintptr) string {
	if errCode == 0 {
		return "<nil>"
	}
	return fmt.Sprintf("Windows error %d", errCode)
}
