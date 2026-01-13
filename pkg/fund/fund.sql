-- 冷资产存储 SQL DDL
-- 采用分片设计: balance_000 ~ balance_127, journal_000 ~ journal_127

-- =============================================================================
-- 余额表模板 (用户热钱包余额的冷存储镜像)
-- 实际表名: balance_000, balance_001, ..., balance_127
-- =============================================================================

CREATE TABLE IF NOT EXISTS `balance_000` (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `user_id` BIGINT NOT NULL COMMENT '用户ID',
    `symbol` VARCHAR(16) NOT NULL COMMENT '资产符号 (USDT/BTC)',
    `available` BIGINT NOT NULL DEFAULT 0 COMMENT '可用余额',
    `locked` BIGINT NOT NULL DEFAULT 0 COMMENT '冻结余额',
    `version` INT NOT NULL DEFAULT 0 COMMENT '乐观锁版本号',
    `updated_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY `uk_user_symbol` (`user_id`, `symbol`),
    KEY `idx_user` (`user_id`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '用户余额表 (分片000)';

-- =============================================================================
-- 流水表模板 (所有余额变更记录)
-- 实际表名: journal_000, journal_001, ..., journal_127
-- =============================================================================

CREATE TABLE IF NOT EXISTS `journal_000` (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `event_id` VARCHAR(64) NOT NULL COMMENT '幂等键',
    `user_id` BIGINT NOT NULL,
    `symbol` VARCHAR(16) NOT NULL,
    `change_type` TINYINT NOT NULL COMMENT '1=冻结,2=解冻,3=划转,4=充值,5=提现,6=手续费',
    `amount` BIGINT NOT NULL COMMENT '变动金额 (正数)',
    `available_before` BIGINT NOT NULL,
    `available_after` BIGINT NOT NULL,
    `locked_before` BIGINT NOT NULL,
    `locked_after` BIGINT NOT NULL,
    `biz_type` VARCHAR(16) NOT NULL COMMENT 'ORDER/TRADE/DEPOSIT/WITHDRAW',
    `biz_id` VARCHAR(64) NOT NULL COMMENT '关联业务ID',
    `created_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY `uk_event_id` (`event_id`),
    KEY `idx_user_symbol` (`user_id`, `symbol`),
    KEY `idx_biz` (`biz_type`, `biz_id`),
    KEY `idx_created` (`created_at`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '余额流水表 (分片000)';

-- =============================================================================
-- 批量创建分表脚本 (存储过程)
-- =============================================================================

DELIMITER /
/

CREATE PROCEDURE create_balance_shards()
BEGIN
    DECLARE i INT DEFAULT 0;
    DECLARE shard_suffix VARCHAR(3);
    DECLARE sql_text VARCHAR(2000);
    
    WHILE i < 128 DO
        SET shard_suffix = LPAD(i, 3, '0');
        
        -- 创建 balance 分表
        SET @sql_text = CONCAT('CREATE TABLE IF NOT EXISTS balance_', shard_suffix, ' LIKE balance_000');
        PREPARE stmt FROM @sql_text;
        EXECUTE stmt;
        DEALLOCATE PREPARE stmt;
        
        -- 创建 journal 分表
        SET @sql_text = CONCAT('CREATE TABLE IF NOT EXISTS journal_', shard_suffix, ' LIKE journal_000');
        PREPARE stmt FROM @sql_text;
        EXECUTE stmt;
        DEALLOCATE PREPARE stmt;
        
        SET i = i + 1;
    END WHILE;
END
/
/

DELIMITER;

-- 执行分表创建
-- CALL create_balance_shards();

-- =============================================================================
-- 简化版: 单表余额 (不分片，适合开发测试)
-- =============================================================================

CREATE TABLE IF NOT EXISTS `balances` (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `user_id` BIGINT NOT NULL,
    `symbol` VARCHAR(16) NOT NULL,
    `available` BIGINT NOT NULL DEFAULT 0,
    `locked` BIGINT NOT NULL DEFAULT 0,
    `version` INT NOT NULL DEFAULT 0,
    `updated_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY `uk_user_symbol` (`user_id`, `symbol`),
    KEY `idx_user` (`user_id`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '用户余额表 (单表版)';

CREATE TABLE IF NOT EXISTS `journals` (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `event_id` VARCHAR(64) NOT NULL,
    `user_id` BIGINT NOT NULL,
    `symbol` VARCHAR(16) NOT NULL,
    `change_type` TINYINT NOT NULL,
    `amount` BIGINT NOT NULL,
    `available_before` BIGINT NOT NULL,
    `available_after` BIGINT NOT NULL,
    `locked_before` BIGINT NOT NULL,
    `locked_after` BIGINT NOT NULL,
    `biz_type` VARCHAR(16) NOT NULL,
    `biz_id` VARCHAR(64) NOT NULL,
    `created_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY `uk_event_id` (`event_id`),
    KEY `idx_user_symbol` (`user_id`, `symbol`),
    KEY `idx_created` (`created_at`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '余额流水表 (单表版)';