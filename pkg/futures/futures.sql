-- 合约规格表
CREATE TABLE IF NOT EXISTS `contract_specs` (
    `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '合约ID',
    `symbol` VARCHAR(32) NOT NULL COMMENT '合约标识: BTCUSDT',
    `base_currency` VARCHAR(16) NOT NULL COMMENT '标的资产: BTC',
    `quote_currency` VARCHAR(16) NOT NULL COMMENT '报价货币: USDT',
    `settle_currency` VARCHAR(16) NOT NULL COMMENT '结算货币: USDT',
    `contract_type` TINYINT NOT NULL DEFAULT 0 COMMENT '0=永续, 1=交割',
    `contract_size` BIGINT NOT NULL COMMENT '合约面值 (精度单位)',
    `tick_size` BIGINT NOT NULL COMMENT '最小价格变动',
    `min_order_qty` BIGINT NOT NULL DEFAULT 0 COMMENT '最小下单量',
    `max_order_qty` BIGINT NOT NULL DEFAULT 0 COMMENT '最大下单量',
    `max_position_qty` BIGINT NOT NULL DEFAULT 0 COMMENT '最大持仓量',
    `max_leverage` INT NOT NULL DEFAULT 100 COMMENT '最大杠杆倍数',
    `initial_margin_rate` BIGINT NOT NULL COMMENT '初始保证金率 (万分比)',
    `maint_margin_rate` BIGINT NOT NULL COMMENT '维持保证金率 (万分比)',
    `funding_interval` BIGINT NOT NULL DEFAULT 28800 COMMENT '资金费结算间隔(秒)',
    `max_funding_rate` BIGINT NOT NULL DEFAULT 75 COMMENT '最大资金费率(万分比)',
    `price_sources` JSON COMMENT '价格来源: ["binance","okx"]',
    `status` TINYINT NOT NULL DEFAULT 0 COMMENT '0=待上线,1=交易中,2=结算中,3=已结算,4=已下架',
    `listed_at` BIGINT NOT NULL DEFAULT 0 COMMENT '上线时间 (unix ms)',
    `expiry_at` BIGINT NOT NULL DEFAULT 0 COMMENT '到期时间 (unix ms), 永续为0',
    `created_at` BIGINT NOT NULL COMMENT '创建时间',
    `updated_at` BIGINT NOT NULL COMMENT '更新时间',
    UNIQUE KEY `uk_symbol` (`symbol`),
    KEY `idx_status` (`status`),
    KEY `idx_contract_type` (`contract_type`),
    KEY `idx_expiry` (`expiry_at`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '合约规格表';

-- 持仓表
CREATE TABLE IF NOT EXISTS `positions` (
    `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `user_id` BIGINT NOT NULL COMMENT '用户ID',
    `symbol` VARCHAR(32) NOT NULL COMMENT '合约标识',
    `size` BIGINT NOT NULL DEFAULT 0 COMMENT '持仓量 (正=多,负=空)',
    `entry_price` BIGINT NOT NULL DEFAULT 0 COMMENT '开仓均价',
    `margin` BIGINT NOT NULL DEFAULT 0 COMMENT '占用保证金',
    `leverage` INT NOT NULL DEFAULT 1 COMMENT '杠杆倍数',
    `realized_pnl` BIGINT NOT NULL DEFAULT 0 COMMENT '累计已实现盈亏',
    `created_at` BIGINT NOT NULL,
    `updated_at` BIGINT NOT NULL,
    UNIQUE KEY `uk_user_symbol` (`user_id`, `symbol`),
    KEY `idx_user` (`user_id`),
    KEY `idx_symbol` (`symbol`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '合约持仓表';

-- 统一订单表
CREATE TABLE IF NOT EXISTS `orders` (
    `id` INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `order_id` BIGINT NOT NULL COMMENT '雪花ID',
    `user_id` BIGINT NOT NULL,
    `symbol` VARCHAR(32) NOT NULL,
    `product_type` VARCHAR(16) NOT NULL COMMENT 'SPOT/FUTURES/OPTIONS',
    `side` TINYINT NOT NULL COMMENT '1=买,2=卖',
    `order_type` TINYINT NOT NULL COMMENT '1=限价,2=市价',
    `price` BIGINT NOT NULL,
    `qty` BIGINT NOT NULL,
    `filled_qty` BIGINT NOT NULL DEFAULT 0,
    `avg_price` BIGINT NOT NULL DEFAULT 0,
    `status` TINYINT NOT NULL DEFAULT 0 COMMENT '0=新建,1=部分成交,2=全部成交,3=已撤销',
    `extra` JSON COMMENT '产品特有字段',
    `created_at` BIGINT NOT NULL,
    `updated_at` BIGINT NOT NULL,
    UNIQUE KEY `uk_order_id` (`order_id`),
    KEY `idx_user_status` (`user_id`, `status`),
    KEY `idx_user_symbol` (`user_id`, `symbol`),
    KEY `idx_created` (`created_at`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '统一订单表';

-- 交割记录表
CREATE TABLE settlement_records (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    symbol VARCHAR(32) NOT NULL,
    settlement_price BIGINT NOT NULL,
    total_positions INT NOT NULL DEFAULT 0,
    total_pnl BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    started_at BIGINT NOT NULL,
    finished_at BIGINT,
    error_msg TEXT,
    INDEX idx_symbol (symbol),
    INDEX idx_started_at (started_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 用户交割明细表
CREATE TABLE settlement_details (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    settlement_id BIGINT UNSIGNED NOT NULL,
    user_id BIGINT NOT NULL,
    symbol VARCHAR(32) NOT NULL,
    side TINYINT NOT NULL,
    size BIGINT NOT NULL,
    entry_price BIGINT NOT NULL,
    settlement_price BIGINT NOT NULL,
    margin BIGINT NOT NULL,
    pnl BIGINT NOT NULL,
    settlement_amount BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    INDEX idx_settlement_id (settlement_id),
    INDEX idx_user_id (user_id),
    INDEX idx_symbol (symbol)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 资金费支付记录
CREATE TABLE funding_payments (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    symbol VARCHAR(32) NOT NULL,
    position_size BIGINT NOT NULL,
    mark_price BIGINT NOT NULL,
    funding_rate BIGINT NOT NULL,
    payment BIGINT NOT NULL,
    funding_time BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    INDEX idx_user_id (user_id),
    INDEX idx_symbol (symbol),
    INDEX idx_funding_time (funding_time)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 资金费率历史
CREATE TABLE funding_rate_history (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    symbol VARCHAR(32) NOT NULL,
    funding_rate BIGINT NOT NULL,
    mark_price BIGINT NOT NULL,
    index_price BIGINT NOT NULL,
    funding_time BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    UNIQUE INDEX idx_symbol_time (symbol, funding_time)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 保险基金余额表
CREATE TABLE insurance_fund_balances (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    currency VARCHAR(16) NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0,
    updated_at BIGINT NOT NULL,
    UNIQUE INDEX idx_currency (currency)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 保险基金流水表
CREATE TABLE insurance_fund_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    currency VARCHAR(16) NOT NULL,
    change_type VARCHAR(32) NOT NULL, -- DEPOSIT/WITHDRAW/LIQUIDATION_PROFIT/BANKRUPT_COVER
    amount BIGINT NOT NULL, -- 正=增加，负=减少
    balance_after BIGINT NOT NULL,
    related_user_id BIGINT DEFAULT 0,
    related_symbol VARCHAR(32) DEFAULT '',
    remark TEXT,
    created_at BIGINT NOT NULL,
    INDEX idx_currency (currency),
    INDEX idx_created_at (created_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;

-- 初始化 USDT 保险池
INSERT INTO
    insurance_fund_balances (currency, balance, updated_at)
VALUES (
        'USDT',
        0,
        UNIX_TIMESTAMP() * 1000
    );