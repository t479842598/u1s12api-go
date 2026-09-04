//go:build !linux && !darwin

package fingerprint

// kernelRelease 其它平台（windows 等）取不到 uname，返回空由 DetectProfile 兜底。
// 本项目部署目标为 linux/darwin；此文件只为保证跨平台可编译，不承诺指纹正确。
func kernelRelease() string { return "" }
