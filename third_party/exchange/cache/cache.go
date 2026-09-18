package cache

import (
	"sync"

	"github.com/quant1x/x/logger"
	"github.com/quant1x/x/util/homedir"
)

const (
	defaultQuant1xDataPath = "~/.quant1x" // 默认的数据路径
)

var (
	globalCacheOnce   sync.Once                // 懒加载锁
	globalCachePath   = defaultQuant1xDataPath // 数据根路径
	onceTemporaryPath = defaultQuant1xDataPath // 临时路径
)

func initPath(path string) {
	finalPath, err := homedir.Expand(path)
	if err != nil {
		logger.Fatalf("%+v", err)
	}
	onceTemporaryPath = path
	globalCachePath = finalPath
}

// InitCachePath 公开给外部调用的初始化路径的函数
//
//	lazyInit和InitCachePath两者只能真正被调用一次
func InitCachePath(path string) {
	globalCacheOnce.Do(func() {
		onceTemporaryPath = path
		initPath(path)
	})
}

// 默认的初始化路径
func lazyInit() {
	initPath(onceTemporaryPath)
}

// DefaultCachePath 数据缓存的根路径
func DefaultCachePath() string {
	globalCacheOnce.Do(lazyInit)
	return globalCachePath
}

// GetMetaPath 元数据缓存路径
func GetMetaPath() string {
	return DefaultCachePath() + "/meta"
}

// GetBlockPath 板块路径
func GetBlockPath() string {
	return GetMetaPath()
}
