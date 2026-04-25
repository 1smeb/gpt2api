package recharge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
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
	ErrPackageUnavailable = errors.New("recharge: package not available")
	ErrChannelDisabled    = errors.New("recharge: pay channel disabled")
	ErrOrderNotFound      = errors.New("recharge: order not found")
	ErrOrderStateInvalid  = errors.New("recharge: order state invalid")
	ErrRechargeDisabled   = errors.New("recharge: recharge is disabled by admin")
	ErrAmountOutOfRange   = errors.New("recharge: amount out of allowed range")
	ErrDailyLimitExceeded = errors.New("recharge: daily limit exceeded")
)

// Service 协调下单、回调入账、查询。
type Service struct {
	dao     *DAO
	billing *billing.Engine
	users   *user.DAO
	signer  *epay.Signer
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
		signer:  epay.NewSigner(ePayCfg.PID, ePayCfg.Key, ePayCfg.SignType),
		cfg:     ePayCfg,
		mail:    mail,
		baseURL: baseURL,
		log:     log.With(zap.String("mod", "recharge")),
	}
}

// Enabled 表示 epay 通道是否已配置完整(运维侧)。
func (s *Service) Enabled() bool {
	return s.cfg.GatewayURL != "" && s.cfg.PID != "" && s.cfg.Key != "" &&
		s.notifyURL() != "" && s.returnURL() != ""
}

func (s *Service) notifyURL() string {
	if strings.TrimSpace(s.cfg.NotifyURL) != "" {
		return strings.TrimSpace(s.cfg.NotifyURL)
	}
	return s.publicURL("/api/public/epay/notify")
}

func (s *Service) returnURL() string {
	if strings.TrimSpace(s.cfg.ReturnURL) != "" {
		return strings.TrimSpace(s.cfg.ReturnURL)
	}
	return s.publicURL("/api/public/epay/return")
}

func (s *Service) publicURL(path string) string {
	base := strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	if base == "" {
		return ""
	}
	return base + path
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
	PayType  string
	ClientIP string
}

// Create 创建订单并生成跳转 URL。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Order, error) {
	if !s.Enabled() {
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
	payURL, err := s.signer.BuildPayURL(
		s.cfg.GatewayURL, outTradeNo, pkg.Name,
		pkg.PriceCNY, s.notifyURL(), s.returnURL(), extra,
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
	pl, err := s.signer.ParseNotify(form)
	if err != nil {
		s.log.Warn("notify signature invalid",
			zap.String("out_trade_no", form.Get("out_trade_no")))
		return "fail", err
	}
	if pl.OutTradeNo == "" {
		err := errors.New("notify out_trade_no empty")
		s.log.Warn("notify order no empty")
		return "fail", err
	}
	if pid := strings.TrimSpace(pl.Raw["pid"]); pid != "" && s.cfg.PID != "" && pid != s.cfg.PID {
		err := errors.New("notify pid mismatch")
		s.log.Warn("notify pid mismatch",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.String("got_pid", pid))
		return "fail", err
	}
	o, err := s.dao.GetByOutTradeNo(ctx, pl.OutTradeNo)
	if err != nil {
		s.log.Warn("notify order not found",
			zap.String("out_trade_no", pl.OutTradeNo))
		return "fail", err
	}

	// completed 表示已支付且已履约,重复通知直接确认。
	if o.Status == StatusCompleted {
		return "success", nil
	}
	if pl.TradeStatus != "TRADE_SUCCESS" {
		// 上游可能先发一笔"等待付款"之类中间状态,这里简单回 success,后续覆盖。
		return "success", nil
	}

	// 金额二次校验:money 是 "元",priceCNY 是 "分"
	if err := verifyAmount(pl.Money, o.PriceCNY); err != nil {
		s.log.Warn("notify amount mismatch",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.String("got_money", pl.Money),
			zap.Int("want_fen", o.PriceCNY))
		return "fail", err
	}

	fulfilledOrder, fulfilled, err := s.settle(ctx, o, pl)
	if err != nil {
		s.log.Error("notify settle failed",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.Error(err))
		return "fail", err
	}
	if fulfilled {
		s.sendPaidMail(ctx, fulfilledOrder)
	}
	return "success", nil
}

// ReturnResult 是 return_url 的展示结果。它只用于用户提示,不能驱动入账。
type ReturnResult struct {
	OutTradeNo  string
	TradeStatus string
	LocalStatus string
	Trusted     bool
	Message     string
}

// HandleReturn 处理浏览器回跳。return_url 不可信,这里只验签和查询本地状态,绝不改订单/加积分。
func (s *Service) HandleReturn(ctx context.Context, form url.Values) ReturnResult {
	outTradeNo := strings.TrimSpace(form.Get("out_trade_no"))
	ret := ReturnResult{
		OutTradeNo: outTradeNo,
		Message:    "支付结果正在确认,请返回账单页刷新。",
	}
	pl, err := s.signer.ParseNotify(form)
	if err != nil {
		s.log.Warn("return signature invalid", zap.String("out_trade_no", outTradeNo))
		return ret
	}
	ret.Trusted = true
	ret.OutTradeNo = pl.OutTradeNo
	ret.TradeStatus = pl.TradeStatus

	o, err := s.dao.GetByOutTradeNo(ctx, pl.OutTradeNo)
	if err != nil {
		s.log.Warn("return order not found", zap.String("out_trade_no", pl.OutTradeNo))
		ret.Message = "订单不存在或尚未同步,请返回账单页刷新。"
		return ret
	}
	ret.LocalStatus = o.Status
	switch o.Status {
	case StatusCompleted:
		ret.Message = "充值已到账。"
	case StatusPaid:
		ret.Message = "支付已确认,系统正在完成入账。"
	case StatusFulfillmentFailed:
		ret.Message = "支付已确认,但入账履约失败,请联系管理员处理。"
	case StatusPending:
		ret.Message = "已收到支付回跳,等待服务端异步通知确认。"
	case StatusExpired:
		ret.Message = "本地订单已超时,如已付款请等待服务端异步通知或联系管理员。"
	case StatusCancelled:
		ret.Message = "本地订单已取消,如已付款请等待服务端异步通知或联系管理员。"
	default:
		ret.Message = "支付结果正在确认,请返回账单页刷新。"
	}
	return ret
}

// settle 单次入账:
//  1. 验签/金额已在调用方完成;
//  2. 先记录支付成功:pending/expired/cancelled -> paid;
//  3. 再在一个事务里执行幂等履约:加积分 + 写流水 + paid -> completed。
func (s *Service) settle(ctx context.Context, o *Order, pl *epay.NotifyPayload) (*Order, bool, error) {
	if err := s.markPaid(ctx, pl); err != nil {
		return nil, false, err
	}
	fulfilledOrder, fulfilled, err := s.fulfillPaidOrder(ctx, o.OutTradeNo)
	if err != nil {
		if markErr := s.markFulfillmentFailed(ctx, o.OutTradeNo); markErr != nil {
			s.log.Error("mark fulfillment failed status failed",
				zap.String("out_trade_no", o.OutTradeNo),
				zap.Error(markErr))
		}
		return nil, false, err
	}
	return fulfilledOrder, fulfilled, nil
}

func (s *Service) markPaid(ctx context.Context, pl *epay.NotifyPayload) error {
	raw := rawDump(pl.Raw)
	// 支付成功回调是权威信号。即便本地 pending 已被用户取消或超时,只要验签和金额通过,仍记录为 paid。
	res, err := s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders
           SET status = ?, trade_no = ?, pay_method = ?, paid_at = NOW(),
               notify_raw = ?
         WHERE out_trade_no = ?
           AND status IN (?, ?, ?)`,
		StatusPaid, pl.TradeNo, pl.Type, raw, pl.OutTradeNo,
		StatusPending, StatusExpired, StatusCancelled)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return nil
	}

	// 已经是 paid/completed/fulfillment_failed 的重复通知只刷新上游信息,不改变主状态。
	res, err = s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders
            SET trade_no = COALESCE(NULLIF(?, ''), trade_no),
                pay_method = COALESCE(NULLIF(?, ''), pay_method),
                notify_raw = ?
          WHERE out_trade_no = ?
            AND status IN (?, ?, ?)`,
		pl.TradeNo, pl.Type, raw, pl.OutTradeNo,
		StatusPaid, StatusCompleted, StatusFulfillmentFailed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	return ErrOrderStateInvalid
}

func (s *Service) fulfillPaidOrder(ctx context.Context, outTradeNo string) (*Order, bool, error) {
	tx, err := s.dao.DB().BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var o Order
	err = tx.GetContext(ctx, &o,
		`SELECT * FROM recharge_orders WHERE out_trade_no = ? FOR UPDATE`, outTradeNo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrOrderNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if o.Status == StatusCompleted {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		committed = true
		return &o, false, nil
	}
	if o.Status != StatusPaid && o.Status != StatusFulfillmentFailed {
		return nil, false, ErrOrderStateInvalid
	}

	refID := fmt.Sprintf("order:%s", o.OutTradeNo)
	var existing int
	if err := tx.GetContext(ctx, &existing,
		`SELECT COUNT(*) FROM credit_transactions WHERE type = ? AND ref_id = ?`,
		billing.KindRecharge, refID); err != nil {
		return nil, false, err
	}
	if existing > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE recharge_orders SET status = ? WHERE id = ?`,
			StatusCompleted, o.ID); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		committed = true
		o.Status = StatusCompleted
		return &o, false, nil
	}

	total := o.TotalCredits()
	if total <= 0 {
		return nil, false, errors.New("recharge credits must be positive")
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE users
            SET credit_balance = credit_balance + ?, version = version + 1
          WHERE id = ? AND deleted_at IS NULL`,
		total, o.UserID)
	if err != nil {
		return nil, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, false, fmt.Errorf("user %d not found", o.UserID)
	}

	var balanceAfter int64
	if err := tx.QueryRowxContext(ctx,
		`SELECT credit_balance FROM users WHERE id = ?`, o.UserID).Scan(&balanceAfter); err != nil {
		return nil, false, err
	}
	remark := fmt.Sprintf("充值:%s", o.Remark)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credit_transactions
            (user_id, key_id, type, amount, balance_after, ref_id, remark)
          VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.UserID, 0, billing.KindRecharge, total, balanceAfter, refID, remark); err != nil {
		return nil, false, err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE recharge_orders
            SET status = ?
          WHERE id = ? AND status IN (?, ?)`,
		StatusCompleted, o.ID, StatusPaid, StatusFulfillmentFailed)
	if err != nil {
		return nil, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, false, ErrOrderStateInvalid
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	committed = true
	o.Status = StatusCompleted
	return &o, true, nil
}

func (s *Service) markFulfillmentFailed(ctx context.Context, outTradeNo string) error {
	_, err := s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders
            SET status = ?
          WHERE out_trade_no = ?
            AND status IN (?, ?)`,
		StatusFulfillmentFailed, outTradeNo, StatusPaid, StatusFulfillmentFailed)
	return err
}

func (s *Service) sendPaidMail(ctx context.Context, o *Order) {
	if o == nil {
		return
	}
	if s.mail != nil && !s.mail.Disabled() {
		if u, err := s.users.GetByID(ctx, o.UserID); err == nil && u.Email != "" {
			subject, html := mailer.RenderPaid(u.Nickname, o.OutTradeNo, o.PriceCNY, o.Credits, o.Bonus, nowUTC())
			s.mail.Send(mailer.Message{To: u.Email, Subject: subject, HTML: html})
		}
	}
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

// AdminForcePaid 管理员手工将 pending 订单置为已支付并入账,或重试 paid/fulfillment_failed 履约。
func (s *Service) AdminForcePaid(ctx context.Context, orderID uint64, actorID uint64) error {
	o, err := s.dao.GetByID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status != StatusPending && o.Status != StatusPaid && o.Status != StatusFulfillmentFailed {
		return ErrOrderStateInvalid
	}
	if o.Status == StatusPending {
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
	}
	fulfilledOrder, fulfilled, err := s.fulfillPaidOrder(ctx, o.OutTradeNo)
	if err != nil {
		if markErr := s.markFulfillmentFailed(ctx, o.OutTradeNo); markErr != nil {
			s.log.Error("mark manual fulfillment failed status failed",
				zap.String("out_trade_no", o.OutTradeNo),
				zap.Uint64("actor_id", actorID),
				zap.Error(markErr))
		}
		return err
	}
	if fulfilled {
		s.sendPaidMail(ctx, fulfilledOrder)
	}
	return nil
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

// verifyAmount 把 "12.00" 和 1200(分) 精确对比,避免 float 舍入误差。
func verifyAmount(money string, wantFen int) error {
	got, err := parseMoneyFen(money)
	if err != nil {
		return err
	}
	if got != wantFen {
		return fmt.Errorf("amount mismatch: got %d fen, want %d", got, wantFen)
	}
	return nil
}

func parseMoneyFen(money string) (int, error) {
	s := strings.TrimSpace(money)
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("invalid money: %q", money)
	}
	parts := strings.SplitN(s, ".", 2)
	yuan, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid money: %w", err)
	}
	fenText := "00"
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 2 {
			return 0, fmt.Errorf("invalid money precision: %q", money)
		}
		fenText = (frac + "00")[:2]
	}
	fen, err := strconv.Atoi(fenText)
	if err != nil {
		return 0, fmt.Errorf("invalid money: %w", err)
	}
	return yuan*100 + fen, nil
}

// nowUTC 抽离以便单测 stub。
var nowUTC = defaultNowUTC
