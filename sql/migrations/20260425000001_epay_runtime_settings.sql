-- +goose Up
-- +goose StatementBegin

-- 易支付 / Z-Pay 运行时配置。已存在则保留用户修改值。
INSERT INTO `system_settings` (`k`, `v`, `description`) VALUES
    ('epay.gateway_url', '',    '易支付页面跳转网关地址,例如 https://pay.example.com/submit.php'),
    ('epay.pid',         '',    '易支付商户 ID'),
    ('epay.key',         '',    '易支付商户密钥,用于 MD5 签名'),
    ('epay.notify_url',  '',    '异步通知地址;留空时优先使用当前访问域名,再回退到 app.base_url + /api/public/epay/notify'),
    ('epay.return_url',  '',    '同步跳转地址;留空时优先使用当前访问域名,再回退到 app.base_url + /api/public/epay/return'),
    ('epay.sign_type',   'MD5', '签名类型,当前支持 MD5')
ON DUPLICATE KEY UPDATE `k` = VALUES(`k`);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 保留数据:删除这些 key 会导致线上支付配置丢失。
-- +goose StatementEnd
