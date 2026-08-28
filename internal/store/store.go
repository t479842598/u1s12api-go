// Package store SQLite 持久化：上游 U1S1 Key、本地分发 Key、请求记录。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store 封装 sqlite 连接。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）数据库并迁移 schema。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 写并发有限，单连接串行化写入最稳。
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS upstream_keys(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT NOT NULL UNIQUE,
			note TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			cooldown_until INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			tokens_per_usd REAL NOT NULL DEFAULT 0,
			daily_free_remaining_usd REAL NOT NULL DEFAULT -1,
			remaining_usd REAL NOT NULL DEFAULT -1,
			free_claim TEXT NOT NULL DEFAULT '',
			quota_checked_at INTEGER NOT NULL DEFAULT 0,
			total_requests INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			last_used_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS local_keys(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			key TEXT NOT NULL UNIQUE,
			note TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			total_requests INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			last_used_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS requests(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			api_key_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			upstream_key_id INTEGER NOT NULL DEFAULT 0,
			stream INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'success',
			http_status INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			client_ip TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model)`,
		`CREATE TABLE IF NOT EXISTS accounts(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			device_token TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '',
			device_id TEXT NOT NULL DEFAULT '',
			device_private_jwk TEXT NOT NULL DEFAULT '',
			device_public_jwk TEXT NOT NULL DEFAULT '',
			device_name TEXT NOT NULL DEFAULT '',
			authorized INTEGER NOT NULL DEFAULT 0,
			last_checkin_at INTEGER NOT NULL DEFAULT 0,
			login_checkin_remaining INTEGER NOT NULL DEFAULT -1,
			total_requests INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// ---- 上游 U1S1 Key ----

// UpstreamKey 上游 key 记录。
type UpstreamKey struct {
	ID                    int64   `json:"id"`
	Key                   string  `json:"key"`
	KeyMasked             string  `json:"key_masked"`
	Note                  string  `json:"note"`
	Status                string  `json:"status"` // active|cooldown|disabled
	CooldownUntil         int64   `json:"cooldown_until"`
	LastError             string  `json:"last_error"`
	Email                 string  `json:"email"`
	TokensPerUSD          float64 `json:"tokens_per_usd"`
	DailyFreeRemainingUSD float64 `json:"daily_free_remaining_usd"`
	RemainingUSD          float64 `json:"remaining_usd"`
	FreeClaim             string  `json:"free_claim"`
	QuotaCheckedAt        int64   `json:"quota_checked_at"`
	TotalRequests         int64   `json:"total_requests"`
	TotalTokens           int64   `json:"total_tokens"`
	CreatedAt             int64   `json:"created_at"`
	LastUsedAt            int64   `json:"last_used_at"`
}

// MaskKey 把 u1s1-xxxxxxxx... 显示为 u1s1-xxxx…xxxx。
func MaskKey(key string) string {
	if len(key) <= 14 {
		return key
	}
	return key[:9] + "…" + key[len(key)-4:]
}

const upstreamKeyCols = `id,key,note,status,cooldown_until,last_error,email,tokens_per_usd,
	daily_free_remaining_usd,remaining_usd,free_claim,quota_checked_at,total_requests,total_tokens,created_at,last_used_at`

func scanUpstreamKey(row interface{ Scan(...any) error }) (*UpstreamKey, error) {
	k := &UpstreamKey{}
	err := row.Scan(&k.ID, &k.Key, &k.Note, &k.Status, &k.CooldownUntil, &k.LastError,
		&k.Email, &k.TokensPerUSD, &k.DailyFreeRemainingUSD, &k.RemainingUSD, &k.FreeClaim,
		&k.QuotaCheckedAt, &k.TotalRequests, &k.TotalTokens, &k.CreatedAt, &k.LastUsedAt)
	if err != nil {
		return nil, err
	}
	k.KeyMasked = MaskKey(k.Key)
	return k, nil
}

// AddUpstreamKey 插入一把 key；重复返回 false。
func (s *Store) AddUpstreamKey(key, note string) (bool, error) {
	key = strings.TrimSpace(key)
	res, err := s.db.Exec(`INSERT OR IGNORE INTO upstream_keys(key,note,created_at) VALUES(?,?,?)`,
		key, strings.TrimSpace(note), time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListUpstreamKeys 全量列出（按创建顺序）。
func (s *Store) ListUpstreamKeys() ([]*UpstreamKey, error) {
	rows, err := s.db.Query(`SELECT ` + upstreamKeyCols + ` FROM upstream_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UpstreamKey
	for rows.Next() {
		k, err := scanUpstreamKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteUpstreamKey 删除。
func (s *Store) DeleteUpstreamKey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM upstream_keys WHERE id=?`, id)
	return err
}

// SetUpstreamKeyStatus 更新状态/冷却/错误信息。
func (s *Store) SetUpstreamKeyStatus(id int64, status string, cooldownUntil time.Time, lastErr string) error {
	until := cooldownUntil.Unix()
	if cooldownUntil.IsZero() {
		until = 0
	}
	_, err := s.db.Exec(`UPDATE upstream_keys SET status=?, cooldown_until=?, last_error=? WHERE id=?`,
		status, until, lastErr, id)
	return err
}

// UpdateUpstreamQuota 写入 /me 配额快照。配额查询成功说明 key 有效：
// 若处于冷却态则一并恢复 active（免费额度已确认可用）。
func (s *Store) UpdateUpstreamQuota(id int64, email string, tokensPerUSD, dailyRemaining, remaining float64, freeClaim string) error {
	_, err := s.db.Exec(`UPDATE upstream_keys SET email=?, tokens_per_usd=?, daily_free_remaining_usd=?,
		remaining_usd=?, free_claim=?, quota_checked_at=?,
		status=CASE WHEN status='cooldown' THEN 'active' ELSE status END,
		cooldown_until=CASE WHEN status='cooldown' THEN 0 ELSE cooldown_until END
		WHERE id=?`, email, tokensPerUSD, dailyRemaining, remaining, freeClaim, time.Now().Unix(), id)
	return err
}

// LatestUpstreamQuotaCheckedAt 全部上游 key 中最近的配额检查时间（unix 秒）；
// 无 key 或从未检查过返回 0。供北京时间 0 点自动刷新判断「今天是否已刷过」。
func (s *Store) LatestUpstreamQuotaCheckedAt() int64 {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(quota_checked_at) FROM upstream_keys`).Scan(&v)
	if err != nil || !v.Valid {
		return 0
	}
	return v.Int64
}

// TouchUpstreamKey 更新最后使用时间与累计计数。
func (s *Store) TouchUpstreamKey(id int64, tokens int64) error {
	_, err := s.db.Exec(`UPDATE upstream_keys SET last_used_at=?, total_requests=total_requests+1,
		total_tokens=total_tokens+? WHERE id=?`, time.Now().Unix(), tokens, id)
	return err
}

// CountUpstreamKeysByStatus 统计各状态数量。
func (s *Store) CountUpstreamKeysByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM upstream_keys GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"total": 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
		out["total"] += n
	}
	return out, rows.Err()
}

// ---- 本地分发 Key ----

// LocalKey 本地 sk- key。
type LocalKey struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Key           string `json:"key"`
	KeyMasked     string `json:"key_masked"`
	Note          string `json:"note"`
	Enabled       bool   `json:"enabled"`
	TotalRequests int64  `json:"total_requests"`
	TotalTokens   int64  `json:"total_tokens"`
	CreatedAt     int64  `json:"created_at"`
	LastUsedAt    int64  `json:"last_used_at"`
}

const localKeyCols = `id,name,key,note,enabled,total_requests,total_tokens,created_at,last_used_at`

func scanLocalKey(row interface{ Scan(...any) error }) (*LocalKey, error) {
	k := &LocalKey{}
	var enabled int
	err := row.Scan(&k.ID, &k.Name, &k.Key, &k.Note, &enabled, &k.TotalRequests, &k.TotalTokens, &k.CreatedAt, &k.LastUsedAt)
	if err != nil {
		return nil, err
	}
	k.Enabled = enabled == 1
	k.KeyMasked = MaskLocalKey(k.Key)
	return k, nil
}

// MaskLocalKey 本地 key 掩码显示。
func MaskLocalKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:7] + "…" + key[len(key)-4:]
}

// CreateLocalKey 新建本地 key（name 冲突时自动加后缀）。
func (s *Store) CreateLocalKey(name, key, note string) (*LocalKey, error) {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "default"
	}
	name = base
	for i := 2; ; i++ {
		var exists int
		err := s.db.QueryRow(`SELECT COUNT(*) FROM local_keys WHERE name=?`, name).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if exists == 0 {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(`INSERT INTO local_keys(name,key,note,created_at) VALUES(?,?,?,?)`,
		name, key, note, now); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT `+localKeyCols+` FROM local_keys WHERE name=?`, name)
	return scanLocalKey(row)
}

// ListLocalKeys 列出全部。
func (s *Store) ListLocalKeys() ([]*LocalKey, error) {
	rows, err := s.db.Query(`SELECT ` + localKeyCols + ` FROM local_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LocalKey
	for rows.Next() {
		k, err := scanLocalKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AuthenticateLocalKey 校验 Bearer/x-api-key，返回 key 名；未命中返回 ""。
func (s *Store) AuthenticateLocalKey(presented string) (string, error) {
	if presented == "" {
		return "", nil
	}
	var name string
	var enabled int
	err := s.db.QueryRow(`SELECT name, enabled FROM local_keys WHERE key=?`, presented).Scan(&name, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if enabled != 1 {
		return "", nil
	}
	_, _ = s.db.Exec(`UPDATE local_keys SET last_used_at=?, total_requests=total_requests+1 WHERE name=?`,
		time.Now().Unix(), name)
	return name, nil
}

// UpdateLocalKey 改名/备注/启停。
func (s *Store) UpdateLocalKey(name string, fields map[string]any) error {
	sets := []string{}
	args := []any{}
	if v, ok := fields["note"]; ok {
		sets = append(sets, "note=?")
		args = append(args, v)
	}
	if v, ok := fields["enabled"]; ok {
		sets = append(sets, "enabled=?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, name)
	_, err := s.db.Exec(`UPDATE local_keys SET `+strings.Join(sets, ",")+` WHERE name=?`, args...)
	return err
}

// DeleteLocalKey 删除。
func (s *Store) DeleteLocalKey(name string) error {
	_, err := s.db.Exec(`DELETE FROM local_keys WHERE name=?`, name)
	return err
}

// GetLocalKeyByName 按名称取本地 key（含完整密钥）。不存在时返回错误。
func (s *Store) GetLocalKeyByName(name string) (*LocalKey, error) {
	row := s.db.QueryRow(`SELECT `+localKeyCols+` FROM local_keys WHERE name=?`, name)
	k, err := scanLocalKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("API Key %q 不存在", name)
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

// ---- 请求记录 ----

// RequestRecord 一条转发记录。
type RequestRecord struct {
	ID            int64   `json:"id"`
	TS            int64   `json:"ts"`
	APIKeyName    string  `json:"api_key_name"`
	Model         string  `json:"model"`
	UpstreamKeyID int64   `json:"upstream_key_id"`
	Stream        bool    `json:"stream"`
	Status        string  `json:"status"`
	HTTPStatus    int     `json:"http_status"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	TotalTokens   int64   `json:"total_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	DurationMS    int64   `json:"duration_ms"`
	Error         string  `json:"error"`
	ClientIP      string  `json:"client_ip"`
}

// InsertRequest 落库。
func (s *Store) InsertRequest(r *RequestRecord) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO requests(ts,api_key_name,model,upstream_key_id,stream,status,
		http_status,input_tokens,output_tokens,total_tokens,cost_usd,duration_ms,error,client_ip)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.TS, r.APIKeyName, r.Model, r.UpstreamKeyID, b2i(r.Stream), r.Status, r.HTTPStatus,
		r.InputTokens, r.OutputTokens, r.TotalTokens, r.CostUSD, r.DurationMS, r.Error, r.ClientIP)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RequestFilter 列表过滤条件。
type RequestFilter struct {
	Model   string
	Status  string
	KeyName string
	Limit   int
	Offset  int
}

// ListRequests 分页查询（新→旧）。
func (s *Store) ListRequests(f RequestFilter) ([]*RequestRecord, error) {
	where, args := requestWhere(f)
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,ts,api_key_name,model,upstream_key_id,stream,status,http_status,
		input_tokens,output_tokens,total_tokens,cost_usd,duration_ms,error,client_ip
		FROM requests ` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RequestRecord{}
	for rows.Next() {
		r := &RequestRecord{}
		var stream int
		if err := rows.Scan(&r.ID, &r.TS, &r.APIKeyName, &r.Model, &r.UpstreamKeyID, &stream,
			&r.Status, &r.HTTPStatus, &r.InputTokens, &r.OutputTokens, &r.TotalTokens,
			&r.CostUSD, &r.DurationMS, &r.Error, &r.ClientIP); err != nil {
			return nil, err
		}
		r.Stream = stream == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRequests 总数。
func (s *Store) CountRequests(f RequestFilter) (int, error) {
	where, args := requestWhere(f)
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM requests `+where, args...).Scan(&n)
	return n, err
}

func requestWhere(f RequestFilter) (string, []any) {
	conds := []string{}
	args := []any{}
	if f.Model != "" {
		conds = append(conds, "model=?")
		args = append(args, f.Model)
	}
	if f.Status != "" {
		conds = append(conds, "status=?")
		args = append(args, f.Status)
	}
	if f.KeyName != "" {
		conds = append(conds, "api_key_name=?")
		args = append(args, f.KeyName)
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// RequestStats 按时间范围聚合请求统计（含模型/API Key 分解）。
func (s *Store) RequestStats(days int) (*RequestStatsResult, error) {
	loc := time.FixedZone("CST", 8*3600)
	var since int64
	if days > 0 {
		since = time.Now().In(loc).AddDate(0, 0, -days).Unix()
	}

	var r RequestStatsResult
	// 总体统计
	err := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='error'  THEN 1 ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),
		COALESCE(AVG(duration_ms),0)
		FROM requests`+
		addWhereSince(since)+`;`, sarg(since)).Scan(
		&r.Total, &r.Success, &r.Error, &r.TotalTokens, &r.AvgDurationMs)
	if err != nil {
		return nil, err
	}

	// 按模型
	if r.ByModel == nil {
		r.ByModel = map[string]RequestStatsEntry{}
	}
	mrows, err := s.db.Query(`SELECT model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0)
		FROM requests`+addWhereSince(since)+` GROUP BY model ORDER BY SUM(total_tokens) DESC;`, sarg(since))
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var model string
		var e RequestStatsEntry
		if err := mrows.Scan(&model, &e.Count, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens); err != nil {
			return nil, err
		}
		r.ByModel[model] = e
	}

	// 按 API Key
	if r.ByAPIKey == nil {
		r.ByAPIKey = map[string]RequestStatsEntry{}
	}
	krows, err := s.db.Query(`SELECT api_key_name, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0)
		FROM requests`+addWhereSince(since)+` GROUP BY api_key_name ORDER BY SUM(total_tokens) DESC;`, sarg(since))
	if err != nil {
		return nil, err
	}
	defer krows.Close()
	for krows.Next() {
		var name string
		var e RequestStatsEntry
		if err := krows.Scan(&name, &e.Count, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens); err != nil {
			return nil, err
		}
		r.ByAPIKey[name] = e
	}

	// 按上游 U1S1 Key（upstream_key_id → 掩码 key 标签）
	if r.ByUpstreamKey == nil {
		r.ByUpstreamKey = map[string]RequestStatsEntry{}
	}
	urows, err := s.db.Query(`SELECT r.upstream_key_id, k.key, COUNT(*), COALESCE(SUM(r.input_tokens),0), COALESCE(SUM(r.output_tokens),0), COALESCE(SUM(r.total_tokens),0)
		FROM requests r LEFT JOIN upstream_keys k ON r.upstream_key_id = k.id
		WHERE r.upstream_key_id > 0`+andWhereSince(since)+`
		GROUP BY r.upstream_key_id ORDER BY SUM(r.total_tokens) DESC;`, sarg(since))
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	for urows.Next() {
		var id int64
		var key string
		var e RequestStatsEntry
		if err := urows.Scan(&id, &key, &e.Count, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens); err != nil {
			return nil, err
		}
		label := fmt.Sprintf("#%d", id)
		if key != "" {
			label = MaskKey(key)
		}
		r.ByUpstreamKey[label] = e
	}

	return &r, nil
}

func andWhereSince(since int64) string {
	if since > 0 {
		return " AND ts>=?"
	}
	return ""
}

// RequestStatsResult 统计结果。
type RequestStatsResult struct {
	Total         int64                        `json:"total"`
	Success       int64                        `json:"success"`
	Error         int64                        `json:"error"`
	TotalTokens   int64                        `json:"total_tokens"`
	AvgDurationMs float64                      `json:"avg_duration_ms"`
	ByModel       map[string]RequestStatsEntry `json:"by_model"`
	ByAPIKey      map[string]RequestStatsEntry `json:"by_api_key"`
	ByUpstreamKey map[string]RequestStatsEntry `json:"by_upstream_key"`
}

// RequestStatsEntry 单条聚合。
type RequestStatsEntry struct {
	Count            int64 `json:"count"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

func addWhereSince(since int64) string {
	if since > 0 {
		return " WHERE ts>=?"
	}
	return ""
}

func sarg(since int64) any {
	if since > 0 {
		return since
	}
	return nil
}

// ClearRequests 清空请求记录。
func (s *Store) ClearRequests() error {
	_, err := s.db.Exec(`DELETE FROM requests`)
	return err
}

// DailyUsage 按天聚合。
type DailyUsage struct {
	Date     string  `json:"date"`
	Requests int64   `json:"requests"`
	TotalTok int64   `json:"total_tokens"`
	CostUSD  float64 `json:"cost_usd"`
}

// DailyUsage 最近 days 天的逐日用量（含今天，缺数据的天补零）。
func (s *Store) DailyUsage(days int) ([]DailyUsage, error) {
	loc := time.FixedZone("CST", 8*3600) // 北京时间对齐免费额度重置点
	today := time.Now().In(loc)
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).
		AddDate(0, 0, -(days - 1)).Unix()

	rows, err := s.db.Query(`SELECT ts, COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0)
		FROM requests WHERE ts>=? GROUP BY date(ts,'unixepoch','+8 hours')`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDate := map[string]DailyUsage{}
	for rows.Next() {
		var ts int64
		var d DailyUsage
		if err := rows.Scan(&ts, &d.Requests, &d.TotalTok, &d.CostUSD); err != nil {
			return nil, err
		}
		d.Date = time.Unix(ts, 0).In(loc).Format("2006-01-02")
		byDate[d.Date] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DailyUsage, 0, days)
	startDay := time.Unix(start, 0).In(loc)
	for i := 0; i < days; i++ {
		ds := startDay.AddDate(0, 0, i).Format("2006-01-02")
		if d, ok := byDate[ds]; ok {
			out = append(out, d)
		} else {
			out = append(out, DailyUsage{Date: ds})
		}
	}
	return out, nil
}

// ModelUsage 按模型聚合。
type ModelUsage struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	TotalTok int64  `json:"total_tokens"`
}

// TopModels 最近 days 天按模型聚合，取前 limit 个。
func (s *Store) TopModels(days, limit int) ([]ModelUsage, error) {
	loc := time.FixedZone("CST", 8*3600)
	start := time.Now().In(loc).AddDate(0, 0, -(days - 1)).Unix()
	rows, err := s.db.Query(`SELECT model, COUNT(*), COALESCE(SUM(total_tokens),0)
		FROM requests WHERE ts>=? GROUP BY model ORDER BY COUNT(*) DESC LIMIT ?`, start, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelUsage{}
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Model, &m.Requests, &m.TotalTok); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UsageSummary 区间汇总。
type UsageSummary struct {
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// SummarySince 从 since（unix 秒）起的汇总。
func (s *Store) SummarySince(since int64) (UsageSummary, error) {
	var u UsageSummary
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0) FROM requests WHERE ts>=?`, since).
		Scan(&u.Requests, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.CostUSD)
	return u, err
}

// RecentRequests 最近 n 条。
func (s *Store) RecentRequests(n int) ([]*RequestRecord, error) {
	return s.ListRequests(RequestFilter{Limit: n})
}

// ---- 官网账号（设备凭证） ----

// Account 官网账号记录（含设备凭证，明文存储与 upstream_keys 一致）。
type Account struct {
	ID                   int64  `json:"id"`
	Email                string `json:"email"`
	EmailMasked          string `json:"email_masked"`
	Password             string `json:"password,omitempty"`
	Note                 string `json:"note"`
	Enabled              bool   `json:"enabled"`
	HasPassword          bool   `json:"has_password"`
	DeviceToken          string `json:"device_token"`
	DeviceTokenMasked    string `json:"device_token_masked"`
	APIKey               string `json:"api_key,omitempty"`
	APIKeyMasked         string `json:"api_key_masked"`
	DeviceID             string `json:"device_id"`
	DevicePrivateJWK     string `json:"device_private_jwk,omitempty"`
	DevicePublicJWK      string `json:"device_public_jwk"`
	DeviceName           string `json:"device_name"`
	Authorized           bool   `json:"authorized"`
	LastCheckinAt        int64  `json:"last_checkin_at"`
	LoginCheckinRemaining int64 `json:"login_checkin_remaining"`
	TotalRequests        int64  `json:"total_requests"`
	TotalTokens          int64  `json:"total_tokens"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
}

const accountCols = `id,email,password,note,enabled,device_token,api_key,device_id,
	device_private_jwk,device_public_jwk,device_name,authorized,last_checkin_at,
	login_checkin_remaining,total_requests,total_tokens,created_at,updated_at`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	a := &Account{}
	var enabled, authorized int
	err := row.Scan(&a.ID, &a.Email, &a.Password, &a.Note, &enabled, &a.DeviceToken, &a.APIKey,
		&a.DeviceID, &a.DevicePrivateJWK, &a.DevicePublicJWK, &a.DeviceName, &authorized,
		&a.LastCheckinAt, &a.LoginCheckinRemaining, &a.TotalRequests, &a.TotalTokens,
		&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.Enabled = enabled == 1
	a.Authorized = authorized == 1
	a.HasPassword = a.Password != ""
	a.Password = "" // 列表不回明文密码
	a.EmailMasked = MaskEmail(a.Email)
	a.APIKeyMasked = MaskKey(a.APIKey)
	a.DeviceTokenMasked = MaskDeviceToken(a.DeviceToken)
	return a, nil
}

// MaskEmail 邮箱掩码：t479842598@foxmail.com → t4***@foxmail.com。
func MaskEmail(email string) string {
	i := strings.IndexByte(email, '@')
	if i < 0 {
		if len(email) <= 2 {
			return email
		}
		return email[:2] + "***"
	}
	local, domain := email[:i], email[i:]
	if len(local) <= 2 {
		return local + "***" + domain
	}
	return local[:2] + "***" + domain
}

// MaskDeviceToken 设备凭证掩码：u1s1d-xxxx…xxxx。
func MaskDeviceToken(tok string) string {
	if len(tok) <= 14 {
		return tok
	}
	return tok[:9] + "…" + tok[len(tok)-4:]
}

// AddAccount 新增账号（email 唯一）。重复返回 false。
func (s *Store) AddAccount(email, password, note string) (bool, error) {
	email = strings.TrimSpace(email)
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT OR IGNORE INTO accounts(email,password,note,created_at,updated_at) VALUES(?,?,?,?,?)`,
		email, password, strings.TrimSpace(note), now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListAccounts 全量列出（按创建顺序）。
func (s *Store) ListAccounts() ([]*Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAccountByEmail 按邮箱取账号（含完整凭证）。不存在时返回错误。
func (s *Store) GetAccountByEmail(email string) (*Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE email=?`, strings.TrimSpace(email))
	a, err := scanAccount(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("账号 %q 不存在", email)
		}
		return nil, err
	}
	return a, nil
}

// GetAccount 按 id 取账号（含完整凭证）。
func (s *Store) GetAccount(id int64) (*Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id=?`, id)
	a, err := scanAccount(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("账号 %d 不存在", id)
		}
		return nil, err
	}
	return a, nil
}

// GetAccountCredential 账号登录凭证（明文邮箱+密码）。列表/扫描路径统一不回明文密码，
// 这里单独直查，供管理端「复制账号/复制密码」后到官网手动登录打卡用。
func (s *Store) GetAccountCredential(id int64) (email, password string, err error) {
	err = s.db.QueryRow(`SELECT email, password FROM accounts WHERE id=?`, id).Scan(&email, &password)
	return
}

// SaveAccountDeviceCredential 写入设备凭证并标记已授权。
func (s *Store) SaveAccountDeviceCredential(id int64, deviceToken, apiKey, deviceID, privJWK, pubJWK, deviceName string) error {
	_, err := s.db.Exec(`UPDATE accounts SET device_token=?, api_key=?, device_id=?,
		device_private_jwk=?, device_public_jwk=?, device_name=?, authorized=1, updated_at=?
		WHERE id=?`,
		deviceToken, apiKey, deviceID, privJWK, pubJWK, deviceName, time.Now().Unix(), id)
	return err
}

// ListAuthorizedEnabledAccounts 返回已授权且启用的账号（供每日签到、设备通道使用）。
func (s *Store) ListAuthorizedEnabledAccounts() ([]*Account, error) {
	rows, err := s.db.Query(`SELECT `+accountCols+` FROM accounts WHERE authorized=1 AND enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountAuthorizedEnabled 已授权且启用账号数。
func (s *Store) CountAuthorizedEnabled() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorized=1 AND enabled=1`).Scan(&n)
	return n, err
}

// UpdateAccount 更新启用/备注/密码。字段 map：enabled bool / note string / password string。
func (s *Store) UpdateAccount(id int64, fields map[string]any) error {
	sets := []string{}
	args := []any{}
	if v, ok := fields["enabled"]; ok {
		sets = append(sets, "enabled=?")
		args = append(args, v)
	}
	if v, ok := fields["note"]; ok {
		sets = append(sets, "note=?")
		args = append(args, v)
	}
	if v, ok := fields["password"]; ok {
		sets = append(sets, "password=?")
		args = append(args, v)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, time.Now().Unix(), id)
	if len(sets) == 0 {
		return nil
	}
	_, err := s.db.Exec(`UPDATE accounts SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

// TouchAccount 记录一次设备通道调用（请求数/令牌/最近使用）。
func (s *Store) TouchAccount(id int64, tokens int64) error {
	_, err := s.db.Exec(`UPDATE accounts SET total_requests=total_requests+1,
		total_tokens=total_tokens+?, updated_at=? WHERE id=?`, tokens, time.Now().Unix(), id)
	return err
}

// MarkAccountCheckin 记录一次签到结果。
func (s *Store) MarkAccountCheckin(id int64, remaining int64) error {
	_, err := s.db.Exec(`UPDATE accounts SET last_checkin_at=?, login_checkin_remaining=?, updated_at=? WHERE id=?`,
		time.Now().Unix(), remaining, time.Now().Unix(), id)
	return err
}

// LatestCheckinAt 全部账号中最近的签到时间（unix 秒）；无则返回 0。供启动补签判断。
func (s *Store) LatestCheckinAt() int64 {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(last_checkin_at) FROM accounts`).Scan(&v)
	if err != nil || !v.Valid {
		return 0
	}
	return v.Int64
}

// DeleteAccount 删除。
func (s *Store) DeleteAccount(id int64) error {
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id=?`, id)
	return err
}
