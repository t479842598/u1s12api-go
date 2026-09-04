// 账号身份绑定（设计 D-06）。
//
// 原则：客户端身份是**设备属性**，不是部署属性。真实世界里一台设备就是一个操作
// 系统、一个内核、一个 Node 版本；如果我们在请求时读全局档案，后台一次热切换就会
// 让所有已授权设备同时"换了系统"——那是真实设备群里不存在的形态。
// 因此授权那一刻把身份快照写进 accounts.device_identity，此后该账号的所有请求都用它。
package server

import (
	"github.com/t479842598/u1s12api-go/internal/fingerprint"
	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// currentIdentityJSON 当前生效身份的 JSON 快照，供授权时入库。
// 序列化失败时返回空串——凭证入库比身份快照重要，不能因此阻断授权。
func (s *Server) currentIdentityJSON() string {
	return fingerprint.IdentityJSON(s.fp.Current())
}

// accountCredential 构造该账号的设备凭证，带上它自己的身份快照。
//
// 快照缺失或损坏时回退当前全局身份（AccountToCredential 内部处理），并顺手回填一次，
// 让老库升级后的账号在第一次使用时就稳定下来。
func (s *Server) accountCredential(acc *store.Account) (*upstream.DeviceCredential, error) {
	cred, err := upstream.AccountToCredential(acc.DeviceToken, acc.DevicePrivateJWK, acc.DevicePublicJWK,
		acc.DeviceIdentity, s.fp.Current())
	if err != nil {
		return nil, err
	}
	if acc.DeviceIdentity == "" {
		s.backfillIdentity(acc.ID, s.currentIdentityJSON())
	}
	return cred, nil
}

// backfillIdentity 回填身份快照；失败只记日志，不影响本次请求。
func (s *Server) backfillIdentity(accountID int64, identityJSON string) {
	if identityJSON == "" {
		return
	}
	if err := s.store.SetAccountDeviceIdentity(accountID, identityJSON); err != nil {
		logger.Warnf("回填账号设备身份快照失败 account=%d: %v", accountID, err)
		return
	}
	logger.Infof("已回填账号设备身份快照 account=%d", accountID)
}
