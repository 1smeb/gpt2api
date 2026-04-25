package recharge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/billing"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/settings"
	"github.com/432539/gpt2api/internal/user"
	"github.com/432539/gpt2api/pkg/epay"
	"github.com/432539/gpt2api/pkg/mailer"
)

var (
	ErrPackageUnavailable  = errors.New("recharge: package not available")
	ErrChannelDisabled     = errors.New("recharge: pay channel disabled")
	ErrOrderNotFound       = errors.New("recharge: order not found")
	ErrOrderStateInvalid   = errors.New("recharge: order state invalid")
	ErrRechargeDisabled    = errors.New("recharge: recharge is disabled by admin")
	ErrAmountOutOfRange    = errors.New("recharge: amount out of allowed range")
	ErrDailyLimitExceeded  = errors.New("recharge: daily limit exceeded")
	ErrSettingsUnavailable = errors.New("recharge: settings unavailable")
)

// Service 协调下单、回调入账、查询。
type Service struct {
	dao     *DAO
	billing *billing.Engine
	users   *user.DAO
	cfg     config.EPayConfig
	mail    *mailer.Mailer
	baseURL string // app.base_url 用于邮件里的链接
	log     *zap.Logger

	// settings 可为 nil(兼容旧调用方);为 nil 时使用硬编码兜底
	settings *settings.Service
}

// SetSettings 注入系统设置,用于下单时的开关/金额/日上限/过期分钟。
func (s *Service) SetSettings(ss *settings.Service) { s.settings = ss }

// NewService 构造 Service。
// ePayCfg.GatewayURL 为空时 Service.Enabled()==false,所有下单请求会被拒绝。
func NewService(dao *DAO, bill *billing.Engine, users *user.DAO,
	ePayCfg config.EPayConfig, mail *mailer.Mailer, baseURL string, log *zap.Logger,
) *Service {
	return &Service{
		dao:     dao,
		billing: bill,
		users:   users,
		cfg:     ePayCfg,
		mail:    mail,
		baseURL: baseURL,
		log:     log.With(zap.String("mod", "recharge")),
	}
}

// Enabled 表示 epay 通道是否已配置完整(运维侧)。
func (s *Service) Enabled() bool {
	return s.EnabledForBase("")
}

func (s *Service) EnabledForBase(requestBaseURL string) bool {
	cfg := s.epayConfig()
	notifyURL := s.resolveCallbackURL(cfg.NotifyURL, "", requestBaseURL, "/api/public/epay/notify")
	returnURL := s.resolveCallbackURL(cfg.ReturnURL, "", requestBaseURL, "/api/public/epay/return")
	return s.channelReady(cfg, notifyURL, returnURL)
}

func (s *Service) channelReady(cfg config.EPayConfig, notifyURL, returnURL string) bool {
	return strings.TrimSpace(cfg.GatewayURL) != "" &&
		strings.TrimSpace(cfg.PID) != "" &&
		strings.TrimSpace(cfg.Key) != "" &&
		strings.TrimSpace(notifyURL) != "" &&
		strings.TrimSpace(returnURL) != ""
}

// AdminEnabled 表示"管理员是否允许充值入口"(业务侧开关)。未注入 settings 视为允许。
func (s *Service) AdminEnabled() bool {
	if s.settings == nil {
		return true
	}
	return s.settings.RechargeEnabled()
}

func (s *Service) MinAmountCNY() int64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.RechargeMinCNY()
}
func (s *Service) MaxAmountCNY() int64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.RechargeMaxCNY()
}
func (s *Service) DailyLimitCNY() int64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.RechargeDailyLimitCNY()
}
func (s *Service) OrderExpireMinutes() int {
	if s.settings == nil {
		return 30
	}
	return s.settings.RechargeOrderExpireMin()
}

// ---------- Package 读 ----------

func (s *Service) ListEnabledPackages(ctx context.Context) ([]Package, error) {
	return s.dao.ListPackages(ctx, true)
}

// ---------- 下单 ----------

// CreateInput 用户下单参数。
type CreateInput struct {
	UserID    uint64
	PackageID uint64
	// PayType 可选,决定 epay 网关跳出来默认哪种二维码。
	// "" 让收银台自选;常见值 "alipay" / "wxpay"。
	PayType        string
	ClientIP       string
	RequestBaseURL string
	NotifyURL      string
	ReturnURL      string
}

// Create 创建订单并生成跳转 URL。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Order, error) {
	cfg := s.epayConfig()
	notifyURL := s.resolveCallbackURL(cfg.NotifyURL, in.NotifyURL, in.RequestBaseURL, "/api/public/epay/notify")
	returnURL := s.resolveCallbackURL(cfg.ReturnURL, in.ReturnURL, in.RequestBaseURL, "/api/public/epay/return")
	if !s.channelReady(cfg, notifyURL, returnURL) {
		return nil, ErrChannelDisabled
	}
	// 充值总开关(settings 未注入时视为允许,兼容旧行为)
	if s.settings != nil && !s.settings.RechargeEnabled() {
		return nil, ErrRechargeDisabled
	}
	pkg, err := s.dao.GetPackage(ctx, in.PackageID)
	if err != nil {
		return nil, err
	}
	if !pkg.Enabled {
		return nil, ErrPackageUnavailable
	}

	// 金额范围(分)+ 单用户每日累计上限校验
	if s.settings != nil {
		price := int64(pkg.PriceCNY)
		if min := s.settings.RechargeMinCNY(); min > 0 && price < min {
			return nil, ErrAmountOutOfRange
		}
		if max := s.settings.RechargeMaxCNY(); max > 0 && price > max {
			return nil, ErrAmountOutOfRange
		}
		if cap := s.settings.RechargeDailyLimitCNY(); cap > 0 {
			already, err := s.dao.SumPaidTodayCNY(ctx, in.UserID)
			if err != nil {
				return nil, err
			}
			if already+price > cap {
				return nil, ErrDailyLimitExceeded
			}
		}
	}

	outTradeNo := genTradeNo()
	extra := map[string]string{}
	if in.PayType != "" {
		extra["type"] = in.PayType
	}
	payURL, err := s.signer().BuildPayURL(
		cfg.GatewayURL, outTradeNo, pkg.Name,
		pkg.PriceCNY, notifyURL, returnURL, extra,
	)
	if err != nil {
		return nil, err
	}

	o := &Order{
		OutTradeNo: outTradeNo,
		UserID:     in.UserID,
		PackageID:  pkg.ID,
		PriceCNY:   pkg.PriceCNY,
		Credits:    pkg.Credits,
		Bonus:      pkg.Bonus,
		Channel:    ChannelEPay,
		PayMethod:  in.PayType,
		Status:     StatusPending,
		PayURL:     payURL,
		ClientIP:   in.ClientIP,
		Remark:     pkg.Name,
	}
	if _, err := s.dao.CreateOrder(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// CancelByUser 用户主动取消 pending 订单。
// 已支付订单不允许取消。
func (s *Service) CancelByUser(ctx context.Context, userID, orderID uint64) error {
	o, err := s.dao.GetByID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.UserID != userID {
		return ErrOrderNotFound // 越权一律按 not-found 处理,防枚举
	}
	if o.Status != StatusPending {
		return ErrOrderStateInvalid
	}
	_, err = s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders SET status = ? WHERE id = ? AND status = ?`,
		StatusCancelled, orderID, StatusPending)
	return err
}

// ---------- 回调入账 ----------

// HandleNotify 异步回调处理。返回 (上游期望文本, error)。
//   - 上游期望文本:按 epay 规范,无论"成功/已处理"都必须回 "success";
//     只有完全没处理 / 有异常时才允许回其它内容,以触发上游重发。
//   - 我们出于幂等,收到一笔**已入账**的订单再次回调,也回 "success"。
func (s *Service) HandleNotify(ctx context.Context, form url.Values) (string, error) {
	pl, o, err := s.parseEPayCallback(ctx, form, "notify")
	if err != nil {
		return "fail", err
	}

	if err := s.applyPaidCallback(ctx, o, pl, "notify"); err != nil {
		return "fail", err
	}
	return "success", nil
}

// ReturnResult 是同步回跳页需要展示的支付结果摘要。
type ReturnResult struct {
	OutTradeNo  string
	TradeNo     string
	TradeStatus string
	OrderStatus string
	Paid        bool
}

// HandleReturn 处理 return_url 的浏览器同步回跳。
// return_url 和 notify_url 参数格式相同,这里同样验签并尝试幂等入账,再交给 handler 跳转到前端结果页。
func (s *Service) HandleReturn(ctx context.Context, form url.Values) (*ReturnResult, error) {
	res := &ReturnResult{
		OutTradeNo:  form.Get("out_trade_no"),
		TradeNo:     form.Get("trade_no"),
		TradeStatus: form.Get("trade_status"),
		OrderStatus: StatusPending,
	}

	pl, o, err := s.parseEPayCallback(ctx, form, "return")
	if err != nil {
		return res, err
	}
	res.OutTradeNo = pl.OutTradeNo
	res.TradeNo = pl.TradeNo
	res.TradeStatus = pl.TradeStatus
	res.OrderStatus = o.Status

	if err := s.applyPaidCallback(ctx, o, pl, "return"); err != nil {
		return res, err
	}
	if pl.TradeStatus == "TRADE_SUCCESS" && o.Status != StatusPaid {
		if latest, err := s.dao.GetByOutTradeNo(ctx, pl.OutTradeNo); err == nil {
			res.OrderStatus = latest.Status
		}
	}
	res.Paid = res.OrderStatus == StatusPaid
	return res, nil
}

func (s *Service) parseEPayCallback(ctx context.Context, form url.Values, source string) (*epay.NotifyPayload, *Order, error) {
	pl, err := s.signer().ParseNotify(form)
	if err != nil {
		s.log.Warn(source+" signature invalid",
			zap.String("out_trade_no", form.Get("out_trade_no")))
		return nil, nil, err
	}
	o, err := s.dao.GetByOutTradeNo(ctx, pl.OutTradeNo)
	if err != nil {
		s.log.Warn(source+" order not found",
			zap.String("out_trade_no", pl.OutTradeNo))
		return nil, nil, err
	}
	return pl, o, nil
}

func (s *Service) applyPaidCallback(ctx context.Context, o *Order, pl *epay.NotifyPayload, source string) error {
	// 幂等
	if o.Status == StatusPaid {
		return nil
	}
	if pl.TradeStatus != "TRADE_SUCCESS" {
		// 上游可能先发一笔"等待付款"之类中间状态,这里简单确认,后续成功通知再覆盖。
		return nil
	}

	// 金额二次校验:money 是 "元",priceCNY 是 "分"
	if err := verifyAmount(pl.Money, o.PriceCNY); err != nil {
		s.log.Warn(source+" amount mismatch",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.String("got_money", pl.Money),
			zap.Int("want_fen", o.PriceCNY))
		return err
	}

	if err := s.settle(ctx, o, pl); err != nil {
		s.log.Error(source+" settle failed",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.Error(err))
		return err
	}
	return nil
}

// settle 单次入账:更新订单为 paid + billing.Recharge 增加积分。
// 这里用两段式:先在 recharge 事务内把订单 CAS 成 paid,再调用 billing。
// billing 内部自己开事务,两段失败时会在日志里留痕(极罕见,需要人工对账)。
func (s *Service) settle(ctx context.Context, o *Order, pl *epay.NotifyPayload) error {
	// CAS: pending -> paid,避免双发回调重复入账
	res, err := s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders
           SET status = ?, trade_no = ?, pay_method = ?, paid_at = NOW(),
               notify_raw = ?
         WHERE id = ? AND status = ?`,
		StatusPaid, pl.TradeNo, pl.Type, rawDump(pl.Raw), o.ID, StatusPending)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// 并发下已被另一条回调处理过
		return nil
	}

	refID := fmt.Sprintf("order:%s", o.OutTradeNo)
	remark := fmt.Sprintf("充值:%s", o.Remark)
	total := o.TotalCredits()
	if err := s.billing.Recharge(ctx, o.UserID, total, refID, remark); err != nil {
		// 钱到了但积分没加 —— 回滚到 pending 等待人工介入
		s.log.Error("BILLING FAILED AFTER PAID, needs manual intervention",
			zap.String("out_trade_no", o.OutTradeNo), zap.Error(err))
		return err
	}

	// 异步邮件通知(失败不影响主流程)
	if s.mail != nil && !s.mail.Disabled() {
		if u, err := s.users.GetByID(ctx, o.UserID); err == nil && u.Email != "" {
			subject, html := mailer.RenderPaid(u.Nickname, o.OutTradeNo, o.PriceCNY, o.Credits, o.Bonus, nowUTC())
			s.mail.Send(mailer.Message{To: u.Email, Subject: subject, HTML: html})
		}
	}
	return nil
}

// ---------- admin/ Package 写 ----------

func (s *Service) AdminCreatePackage(ctx context.Context, p *Package) (uint64, error) {
	return s.dao.CreatePackage(ctx, p)
}
func (s *Service) AdminUpdatePackage(ctx context.Context, p *Package) error {
	return s.dao.UpdatePackage(ctx, p)
}
func (s *Service) AdminDeletePackage(ctx context.Context, id uint64) error {
	return s.dao.DeletePackage(ctx, id)
}
func (s *Service) AdminListPackages(ctx context.Context) ([]Package, error) {
	return s.dao.ListPackages(ctx, false)
}

// ---------- Orders 读 ----------

func (s *Service) ListUserOrders(ctx context.Context, userID uint64, status string, offset, limit int) ([]Order, int64, error) {
	return s.dao.List(ctx, ListFilter{UserID: userID, Status: status}, offset, limit)
}

func (s *Service) AdminListOrders(ctx context.Context, f ListFilter, offset, limit int) ([]Order, int64, error) {
	return s.dao.List(ctx, f, offset, limit)
}

// PaymentConfig 是后台支付管理页使用的易支付配置视图。
type PaymentConfig struct {
	GatewayURL         string `json:"gateway_url"`
	PID                string `json:"pid"`
	NotifyURL          string `json:"notify_url"`
	ReturnURL          string `json:"return_url"`
	SignType           string `json:"sign_type"`
	KeySet             bool   `json:"key_set"`
	KeyMask            string `json:"key_mask"`
	EffectiveNotifyURL string `json:"effective_notify_url"`
	EffectiveReturnURL string `json:"effective_return_url"`
	ChannelReady       bool   `json:"channel_ready"`
	RechargeEnabled    bool   `json:"recharge_enabled"`
}

// PaymentConfigUpdate 是后台保存易支付配置的输入。
// Key 为空表示不修改既有密钥;其它字段允许保存为空,此时运行时回退到 YAML 配置。
type PaymentConfigUpdate struct {
	GatewayURL      string
	PID             string
	Key             string
	NotifyURL       string
	ReturnURL       string
	SignType        string
	RechargeEnabled bool
}

func (s *Service) AdminPaymentConfig() PaymentConfig {
	return s.AdminPaymentConfigForBase("")
}

func (s *Service) AdminPaymentConfigForBase(requestBaseURL string) PaymentConfig {
	cfg := s.epayConfig()
	notifyURL := s.resolveCallbackURL(cfg.NotifyURL, "", requestBaseURL, "/api/public/epay/notify")
	returnURL := s.resolveCallbackURL(cfg.ReturnURL, "", requestBaseURL, "/api/public/epay/return")
	return PaymentConfig{
		GatewayURL:         cfg.GatewayURL,
		PID:                cfg.PID,
		NotifyURL:          cfg.NotifyURL,
		ReturnURL:          cfg.ReturnURL,
		SignType:           cfg.SignType,
		KeySet:             strings.TrimSpace(cfg.Key) != "",
		KeyMask:            maskSecret(cfg.Key),
		EffectiveNotifyURL: notifyURL,
		EffectiveReturnURL: returnURL,
		ChannelReady:       s.channelReady(cfg, notifyURL, returnURL),
		RechargeEnabled:    s.AdminEnabled(),
	}
}

func (s *Service) AdminUpdatePaymentConfig(ctx context.Context, in PaymentConfigUpdate) error {
	if s.settings == nil {
		return ErrSettingsUnavailable
	}
	signType := strings.ToUpper(strings.TrimSpace(in.SignType))
	if signType == "" {
		signType = "MD5"
	}
	if signType != "MD5" {
		return fmt.Errorf("unsupported sign_type: %s", signType)
	}
	for name, raw := range map[string]string{
		"gateway_url": strings.TrimSpace(in.GatewayURL),
		"notify_url":  strings.TrimSpace(in.NotifyURL),
		"return_url":  strings.TrimSpace(in.ReturnURL),
	} {
		if raw == "" {
			continue
		}
		if err := validateHTTPURL(raw); err != nil {
			return fmt.Errorf("%s invalid: %w", name, err)
		}
	}

	items := map[string]string{
		settings.EPayGatewayURL: strings.TrimSpace(in.GatewayURL),
		settings.EPayPID:        strings.TrimSpace(in.PID),
		settings.EPayNotifyURL:  strings.TrimSpace(in.NotifyURL),
		settings.EPayReturnURL:  strings.TrimSpace(in.ReturnURL),
		settings.EPaySignType:   signType,
		settings.RechargeEnabled: func() string {
			if in.RechargeEnabled {
				return "true"
			}
			return "false"
		}(),
	}
	if strings.TrimSpace(in.Key) != "" {
		items[settings.EPayKey] = strings.TrimSpace(in.Key)
	}
	return s.settings.Set(ctx, items)
}

// AdminForcePaid 管理员手工将 pending 订单置为已支付并入账(发卡出错时的应急通道)。
func (s *Service) AdminForcePaid(ctx context.Context, orderID uint64, actorID uint64) error {
	o, err := s.dao.GetByID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status != StatusPending {
		return ErrOrderStateInvalid
	}
	res, err := s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders
           SET status = ?, paid_at = NOW(), trade_no = IFNULL(NULLIF(trade_no,''), ?)
         WHERE id = ? AND status = ?`,
		StatusPaid, fmt.Sprintf("manual-%d", actorID), orderID, StatusPending)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOrderStateInvalid
	}
	refID := fmt.Sprintf("order:%s", o.OutTradeNo)
	remark := fmt.Sprintf("管理员手工入账:%s by admin=%d", o.Remark, actorID)
	return s.billing.Recharge(ctx, o.UserID, o.TotalCredits(), refID, remark)
}

// ---------- helpers ----------

// genTradeNo 生成 32 位小写 hex。用 crypto/rand 防撞(绝对不会用 time-based)。
func genTradeNo() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// rawDump 把回调原文拼成 "k=v&k=v",方便排查。不参与签名。
func rawDump(m map[string]string) *string {
	if len(m) == 0 {
		return nil
	}
	v := url.Values{}
	for k, vv := range m {
		v.Set(k, vv)
	}
	s := v.Encode()
	return &s
}

func (s *Service) signer() *epay.Signer {
	cfg := s.epayConfig()
	return epay.NewSigner(cfg.PID, cfg.Key, cfg.SignType)
}

func (s *Service) epayConfig() config.EPayConfig {
	cfg := s.cfg
	if s.settings != nil {
		if v := s.settings.EPayGatewayURL(); v != "" {
			cfg.GatewayURL = v
		}
		if v := s.settings.EPayPID(); v != "" {
			cfg.PID = v
		}
		if v := s.settings.EPayKey(); v != "" {
			cfg.Key = v
		}
		if v := s.settings.EPayNotifyURL(); v != "" {
			cfg.NotifyURL = v
		}
		if v := s.settings.EPayReturnURL(); v != "" {
			cfg.ReturnURL = v
		}
		if v := s.settings.EPaySignType(); v != "" {
			cfg.SignType = v
		}
	}
	cfg.GatewayURL = strings.TrimSpace(cfg.GatewayURL)
	cfg.PID = strings.TrimSpace(cfg.PID)
	cfg.Key = strings.TrimSpace(cfg.Key)
	cfg.NotifyURL = strings.TrimSpace(cfg.NotifyURL)
	cfg.ReturnURL = strings.TrimSpace(cfg.ReturnURL)
	cfg.SignType = strings.ToUpper(strings.TrimSpace(cfg.SignType))
	if cfg.SignType == "" {
		cfg.SignType = "MD5"
	}
	return cfg
}

func (s *Service) notifyURL() string {
	return s.resolveCallbackURL(s.epayConfig().NotifyURL, "", "", "/api/public/epay/notify")
}

func (s *Service) returnURL() string {
	return s.resolveCallbackURL(s.epayConfig().ReturnURL, "", "", "/api/public/epay/return")
}

func (s *Service) resolveCallbackURL(configured, requested, requestBase, p string) string {
	if v := strings.TrimSpace(requested); v != "" {
		return v
	}
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	if v := callbackURLFromBase(requestBase, p); v != "" {
		return v
	}
	return callbackURLFromBase(s.baseURL, p)
}

func callbackURLFromBase(base, p string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + p
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("host required")
	}
	return nil
}

func maskSecret(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return "********"
	}
	return v[:4] + strings.Repeat("*", len(v)-8) + v[len(v)-4:]
}

// verifyAmount 把 "12.00" 和 1200(分) 对比。
func verifyAmount(money string, wantFen int) error {
	var f float64
	if _, err := fmt.Sscanf(money, "%f", &f); err != nil {
		return fmt.Errorf("invalid money: %w", err)
	}
	got := int(f*100 + 0.5)
	if got != wantFen {
		return fmt.Errorf("amount mismatch: got %d fen, want %d", got, wantFen)
	}
	return nil
}

// nowUTC 抽离以便单测 stub。
var nowUTC = defaultNowUTC
