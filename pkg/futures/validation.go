// 文件: pkg/futures/validation.go
// 合约参数验证

package futures

import "errors"

// ValidateCreateRequest 验证创建请求
func ValidateCreateRequest(req *CreateContractRequest) error {
	if req.Symbol == "" {
		return errors.New("symbol is required")
	}
	if req.BaseCurrency == "" || req.QuoteCurrency == "" {
		return errors.New("base/quote currency is required")
	}
	if req.SettleCurrency == "" {
		req.SettleCurrency = req.QuoteCurrency // 默认用报价货币结算
	}
	if req.ContractSize <= 0 {
		return errors.New("contract size must be positive")
	}
	if req.TickSize <= 0 {
		return errors.New("tick size must be positive")
	}
	if req.MaxLeverage <= 0 || req.MaxLeverage > 200 {
		return errors.New("max leverage must be between 1 and 200")
	}
	if req.InitialMarginRate <= 0 {
		// 自动计算: 初始保证金率 = 1/杠杆
		req.InitialMarginRate = int64(RatePrecision / req.MaxLeverage)
	}
	if req.MaintMarginRate <= 0 {
		// 默认维持保证金率 = 初始保证金率 / 2
		req.MaintMarginRate = req.InitialMarginRate / 2
	}
	if req.MaintMarginRate >= req.InitialMarginRate {
		return errors.New("maint margin rate must be less than initial margin rate")
	}
	if req.ContractType == TypePerpetual {
		if req.FundingInterval <= 0 {
			req.FundingInterval = 8 * 3600 // 默认 8 小时
		}
		if req.MaxFundingRate <= 0 {
			req.MaxFundingRate = 75 // 默认 0.75%
		}
	}
	if req.ContractType == TypeDelivery && req.ExpiryAt <= 0 {
		return errors.New("delivery contract requires expiry time")
	}
	if len(req.PriceSources) == 0 {
		return errors.New("at least one price source is required")
	}
	return nil
}
