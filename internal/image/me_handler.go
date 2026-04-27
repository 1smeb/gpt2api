package image

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/432539/gpt2api/internal/middleware"
	"github.com/432539/gpt2api/pkg/resp"
)

// MeHandler 面向当前用户的图片任务只读接口(JWT 鉴权)。
// 与 /v1/images/tasks/:id(API Key 鉴权)共享同一张 image_tasks 表,
// 只是入口改到 /api/me/images/* 便于前端面板调用。
type MeHandler struct{ dao *DAO }

// NewMeHandler 构造。
func NewMeHandler(dao *DAO) *MeHandler { return &MeHandler{dao: dao} }

// taskView 是对外返回的视图结构,解码 JSON 列 + 隐藏内部字段。
type taskView struct {
	ID             uint64    `json:"id"`
	TaskID         string    `json:"task_id"`
	UserID         uint64    `json:"user_id"`
	ModelID        uint64    `json:"model_id"`
	AccountID      uint64    `json:"account_id"`
	Prompt         string    `json:"prompt"`
	N              int       `json:"n"`
	Size           string    `json:"size"`
	Upscale        string    `json:"upscale,omitempty"`
	Status         string    `json:"status"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreditCost     int64     `json:"credit_cost"`
	ImageURLs      []string  `json:"image_urls"`
	FileIDs        []string  `json:"file_ids,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

func toView(t *Task) taskView {
	urls := t.DecodeResultURLs()
	fids := t.DecodeFileIDs()
	for i, id := range fids {
		fids[i] = strings.TrimPrefix(id, "sed:")
	}
	// 关键:对外暴露的图片 URL 一律改成本地代理地址,避免:
	//   1) chatgpt.com estuary 防盗链 → 浏览器 403
	//   2) 上游 15 分钟签名过期 → 历史图片 404
	// 代理收到请求后会再走一次 ImageDownloadURL 现取签名,
	// 所以历史任务的旧 URL 即便已失效也照样能回放。
	if len(urls) > 0 {
		urls = BuildProxyURLs(t.TaskID, urls)
	} else if len(fids) > 0 {
		// 极少数老数据 result_urls 为空但 file_ids 完整:
		// 同样按 idx 走代理,代理端能根据 file_ids 现取签名 URL。
		urls = make([]string, len(fids))
		for i := range fids {
			urls[i] = BuildProxyURL(t.TaskID, i, "")
		}
	}
	return taskView{
		ID: t.ID, TaskID: t.TaskID, UserID: t.UserID, ModelID: t.ModelID,
		AccountID: t.AccountID, Prompt: t.Prompt, N: t.N, Size: t.Size,
		Upscale: t.Upscale,
		Status: t.Status, ConversationID: t.ConversationID, Error: t.Error,
		CreditCost: t.CreditCost, ImageURLs: urls, FileIDs: fids,
		CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
	}
}

// GET /api/me/images/tasks
// 查询参数:
//   limit(默认 20,上限 100), offset
//   status            = queued | dispatched | running | success | failed
//   keyword           = prompt 模糊匹配
//   start_at, end_at  = 时间区间;支持 RFC3339、"2006-01-02 15:04:05"、"2006-01-02"
func (h *MeHandler) List(c *gin.Context) {
	uid := middleware.UserID(c)
	if uid == 0 {
		resp.Unauthorized(c, "not logged in")
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	f := UserTaskFilter{
		Status:  strings.TrimSpace(c.Query("status")),
		Keyword: strings.TrimSpace(c.Query("keyword")),
	}
	if t, ok := parseFilterTime(c.Query("start_at")); ok {
		f.Since = t
	}
	if t, ok := parseFilterTime(c.Query("end_at")); ok {
		// end_at 设计为闭区间("到这一天"),DAO 用前闭后开,所以这里 +1 秒兜底。
		f.Until = t.Add(time.Second)
	}

	tasks, total, err := h.dao.ListByUserFiltered(c.Request.Context(), uid, f, limit, offset)
	if err != nil {
		resp.Internal(c, err.Error())
		return
	}
	items := make([]taskView, 0, len(tasks))
	for i := range tasks {
		items = append(items, toView(&tasks[i]))
	}
	resp.OK(c, gin.H{
		"items":  items,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// parseFilterTime 兼容前端常见的几种时间字面量写法,所有字符串都按
// 服务器本地时区解析,匹配 image_tasks.created_at 的 DATETIME 语义。
func parseFilterTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// GET /api/me/images/tasks/:id
func (h *MeHandler) Get(c *gin.Context) {
	uid := middleware.UserID(c)
	if uid == 0 {
		resp.Unauthorized(c, "not logged in")
		return
	}
	id := c.Param("id")
	if id == "" {
		resp.Fail(c, 40000, "task id required")
		return
	}
	t, err := h.dao.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resp.Fail(c, 40400, "task not found")
			return
		}
		resp.Internal(c, err.Error())
		return
	}
	if t.UserID != uid {
		resp.Fail(c, 40400, "task not found")
		return
	}
	resp.OK(c, toView(t))
}
