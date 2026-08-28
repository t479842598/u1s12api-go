// 设备授权流程：对官网账号发起设备登录（生成 EC P-256 密钥 → 调 /auth/device/start
// → 拿 verify_url + poll_secret → 用户浏览器登录批准 → 回来点「我已授权」→ 轮询
// /auth/device/poll 拿设备凭证入库）。
// 与官方 u1s1-cli login.js 流程对齐。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/t479842598/u1s12api-go/internal/store"
	"github.com/t479842598/u1s12api-go/internal/upstream"
)

// pendingDevice 一次进行中的设备授权（内存态，不落盘；服务重启需重新授权）。
type pendingDevice struct {
	accountID  int64
	pollSecret string
	interval   int
	expiresIn  int
	privJWK    *upstream.DeviceJWK
	pubJWK     *upstream.DeviceJWK
	deviceName string
	startedAt  time.Time
}

// deviceMu 保护 pendingDevices 与签到互斥。
type pendingDeviceMap struct {
	mu   sync.Mutex
	m    map[int64]*pendingDevice
}

func (p *pendingDeviceMap) put(d *pendingDevice) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[int64]*pendingDevice{}
	}
	p.m[d.accountID] = d
}

func (p *pendingDeviceMap) get(id int64) (*pendingDevice, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		return nil, false
	}
	d, ok := p.m[id]
	return d, ok
}

func (p *pendingDeviceMap) del(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		return
	}
	delete(p.m, id)
}

// handleDeviceStart 对账号发起设备登录，返回 verify_url + 倒计时。
func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	acc, err := s.store.GetAccount(id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}
	dc := s.deviceClient()
	if dc == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "设备客户端不可用（检查上游地址/出口代理）")
		return
	}
	// 设备名带账号 email，便于在批准页识别。
	devName := "u1s12api-" + acc.Email
	if len(devName) > 60 {
		devName = devName[:60]
	}
	privJWK, pubJWK, start, err := dc.StartDeviceLoginGenerate(r.Context(), devName, s.getSettings().U1S1Version)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "发起设备登录失败: "+err.Error())
		return
	}
	s.pending.put(&pendingDevice{
		accountID:  id,
		pollSecret: start.PollSecret,
		interval:   start.Interval,
		expiresIn:  start.ExpiresIn,
		privJWK:    privJWK,
		pubJWK:     pubJWK,
		deviceName: devName,
		startedAt:  time.Now(),
	})
	logger.Infof("设备授权发起: account=%s verify_url=%s", acc.Email, start.VerifyURL)
	writeAPIData(w, http.StatusOK, map[string]any{
		"account_id": id,
		"verify_url": start.VerifyURL,
		"expires_in": start.ExpiresIn,
		"interval":   start.Interval,
	})
}

// handleDeviceConfirm 用户已在浏览器批准，回来点「我已授权」：轮询拿凭证入库。
func (s *Server) handleDeviceConfirm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "无效 id")
		return
	}
	pd, ok := s.pending.get(id)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "该账号没有进行中的设备授权，请先点「授权」")
		return
	}
	dc := s.deviceClient()
	if dc == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "设备客户端不可用")
		return
	}
	// 轮询（阻塞直到批准或超时），客户端带超时上下文。
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(pd.expiresIn+5)*time.Second)
	defer cancel()
	resp, err := dc.PollDeviceLogin(ctx, pd.pollSecret, pd.interval, pd.expiresIn)
	if err != nil {
		writeAPIError(w, http.StatusGatewayTimeout, "轮询设备批准失败: "+err.Error())
		return
	}
	s.pending.del(id)
	if resp == nil {
		writeAPIError(w, http.StatusRequestTimeout, "设备尚未批准或链接已过期（expires_in 秒内需在浏览器完成批准）")
		return
	}
	privJSON, _ := json.Marshal(pd.privJWK)
	pubJSON, _ := json.Marshal(pd.pubJWK)
	if err := s.store.SaveAccountDeviceCredential(id, resp.DeviceToken, resp.APIKey, resp.DeviceID,
		string(privJSON), string(pubJSON), pd.deviceName); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "保存设备凭证失败: "+err.Error())
		return
	}
	// 授权后立刻做一次签到（调 /v1/me 触发加量包发放）。
	_ = s.checkinOne(id)
	logger.Infof("设备授权成功: account=%d device_id=%s", id, resp.DeviceID)
	writeAPIData(w, http.StatusOK, map[string]any{
		"authorized":   true,
		"device_id":    resp.DeviceID,
		"api_key":      store.MaskKey(resp.APIKey),
		"device_token": store.MaskDeviceToken(resp.DeviceToken),
	})
}

// deviceClient 当前上游设备客户端（复用配置，不单独重建）。
func (s *Server) deviceClient() *upstream.DeviceClient {
	cfg := s.getSettings()
	return upstream.NewDeviceClient(cfg.UpstreamBaseURL, cfg.EgressProxyURL,
		func() string { return s.getSettings().U1S1Version })
}
