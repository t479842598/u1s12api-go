//go:build linux || darwin

package fingerprint

import "golang.org/x/sys/unix"

// kernelRelease 取 uname -r 的等价值，对应 node 的 os.release()。
//
// 必须是**内核**版本而不是产品版本：官方 UA 用的就是 os.release()，
// Linux 上形如 "6.17.0-1018-oracle"，macOS 上形如 "25.6.0"（Darwin 内核版本，
// 不是 sw_vers 的 "15.x"）。取错来源会让 UA 与 platform 头互相矛盾。
//
// 用 x/sys/unix 而不是 stdlib syscall：Go 在 darwin 上不再导出 syscall.Uname/Utsname。
func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}
