-- +goose Up
-- +goose StatementBegin

ALTER TABLE `recharge_orders`
    MODIFY `status` VARCHAR(32) NOT NULL DEFAULT 'pending'
        COMMENT 'pending | paid | completed | expired | cancelled | fulfillment_failed';

UPDATE `recharge_orders` ro
   SET ro.`status` = 'completed'
 WHERE ro.`status` = 'paid'
   AND EXISTS (
       SELECT 1
         FROM `credit_transactions` ct
        WHERE ct.`type` = 'recharge'
          AND ct.`ref_id` = CONCAT('order:', ro.`out_trade_no`)
   );

-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin

UPDATE `recharge_orders`
   SET `status` = 'failed'
 WHERE `status` = 'fulfillment_failed';

ALTER TABLE `recharge_orders`
    MODIFY `status` VARCHAR(16) NOT NULL DEFAULT 'pending'
        COMMENT 'pending | paid | expired | cancelled | failed';

-- +goose StatementEnd
