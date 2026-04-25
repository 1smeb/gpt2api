package recharge

import (
	"errors"
	"html"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/middleware"
	"github.com/432539/gpt2api/pkg/resp"
)

// Handler 实现用户/公开 端点。
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// GET /api/recharge/packages
// 返回已启用的套餐 + 通道状态。未登录也可访问(方便前端登录页展示定价)。
func (h *Handler) ListPackages(c *gin.Context) {
	pkgs, err := h.svc.ListEnabledPackages(c.Request.Context())
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{
		"items":          pkgs,
		"enabled":        h.svc.Enabled() && h.svc.AdminEnabled(),
		"channel_ready":  h.svc.Enabled(),
		"admin_enabled":  h.svc.AdminEnabled(),
		"min_cny":        h.svc.MinAmountCNY(),
		"max_cny":        h.svc.MaxAmountCNY(),
		"daily_limit":    h.svc.DailyLimitCNY(),
		"expire_minutes": h.svc.OrderExpireMinutes(),
	})
}

// POST /api/recharge/orders
// body: { package_id, pay_type }
func (h *Handler) CreateOrder(c *gin.Context) {
	uid := middleware.UserID(c)
	if uid == 0 {
		resp.Unauthorized(c, "unauthorized")
		return
	}
	var req struct {
		PackageID uint64 `json:"package_id" binding:"required"`
		PayType   string `json:"pay_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, err.Error())
		return
	}
	o, err := h.svc.Create(c.Request.Context(), CreateInput{
		UserID:    uid,
		PackageID: req.PackageID,
		PayType:   req.PayType,
		ClientIP:  c.ClientIP(),
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrChannelDisabled):
			resp.Fail(c, 40301, "支付通道未配置,请联系管理员")
		case errors.Is(err, ErrRechargeDisabled):
			resp.Forbidden(c, "管理员已关闭充值")
		case errors.Is(err, ErrAmountOutOfRange):
			resp.BadRequest(c, "该套餐金额不在允许的充值范围内")
		case errors.Is(err, ErrDailyLimitExceeded):
			resp.BadRequest(c, "已达到今日累计充值上限")
		case errors.Is(err, ErrPackageUnavailable), errors.Is(err, ErrNotFound):
			resp.NotFound(c, "套餐不存在或已下架")
		default:
			resp.Internal(c, err.Error())
		}
		return
	}
	resp.OK(c, o)
}

// GET /api/recharge/orders
func (h *Handler) ListMyOrders(c *gin.Context) {
	uid := middleware.UserID(c)
	if uid == 0 {
		resp.Unauthorized(c, "unauthorized")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	rows, total, err := h.svc.ListUserOrders(c.Request.Context(), uid, c.Query("status"), offset, limit)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	resp.OK(c, gin.H{"items": rows, "total": total, "limit": limit, "offset": offset})
}

// POST /api/recharge/orders/:id/cancel
func (h *Handler) CancelOrder(c *gin.Context) {
	uid := middleware.UserID(c)
	if uid == 0 {
		resp.Unauthorized(c, "unauthorized")
		return
	}
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.CancelByUser(c.Request.Context(), uid, id); err != nil {
		switch {
		case errors.Is(err, ErrOrderStateInvalid):
			resp.Conflict(c, "订单状态不可取消")
		case errors.Is(err, ErrOrderNotFound), errors.Is(err, ErrNotFound):
			resp.NotFound(c, "订单不存在")
		default:
			resp.Internal(c, err.Error())
		}
		return
	}
	resp.OK(c, gin.H{"ok": true})
}

// ---------- 公开的回调入口(不鉴权,走签名校验) ----------

// POST /api/public/epay/notify
// GET  /api/public/epay/notify
// 按上游 ePay 规范,**响应必须是裸 "success"/"fail" 字符串**,不要被 resp 包装。
func (h *Handler) EPayNotify(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.String(200, "fail")
		return
	}
	// ePay 可能 GET 也可能 POST,合并两种 values
	form := c.Request.Form
	text, _ := h.svc.HandleNotify(c.Request.Context(), form)
	c.String(200, text)
}

// GET /api/public/epay/return
// 浏览器回跳页只给用户看结果,不能触发入账。
func (h *Handler) EPayReturn(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.Data(200, "text/html; charset=utf-8", []byte(renderReturnPage(ReturnResult{
			Message: "支付结果解析失败,请返回账单页刷新。",
		})))
		return
	}
	ret := h.svc.HandleReturn(c.Request.Context(), c.Request.Form)
	c.Data(200, "text/html; charset=utf-8", []byte(renderReturnPage(ret)))
}

func renderReturnPage(ret ReturnResult) string {
	return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>支付结果</title>
  <style>
    body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f6f8fb;color:#1f2937}
    .card{max-width:560px;margin:12vh auto;padding:32px;background:#fff;border-radius:16px;box-shadow:0 18px 50px rgba(15,23,42,.12)}
    h1{margin:0 0 12px;font-size:24px}
    p{line-height:1.7;color:#4b5563}
    dl{display:grid;grid-template-columns:96px 1fr;gap:10px 14px;margin:20px 0;color:#374151}
    dt{color:#6b7280}
    code{font-size:12px;background:#eef2ff;padding:2px 6px;border-radius:5px;word-break:break-all}
    .note{font-size:13px;color:#6b7280;background:#f9fafb;border-radius:10px;padding:12px;margin:18px 0}
    a{display:inline-block;margin-top:8px;background:#2563eb;color:#fff;text-decoration:none;padding:10px 16px;border-radius:10px}
  </style>
</head>
<body>
  <main class="card">
    <h1>支付结果</h1>
    <p>` + html.EscapeString(ret.Message) + `</p>
    <a href="/personal/billing">返回账单页</a>
  </main>
</body>
</html>`
}
