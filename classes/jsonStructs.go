package cls

import "fmt"

// struct format of request to start a bot
type StartBotParam struct {
	UserID    string  `json:"userID"`
	BotID     int     `json:"botID"`
	Exchange  string  `json:"exchange"`
	ApiKey    string  `json:"apikey"`
	ApiSecret string  `json:"apisecret"`
	TraderUID string  `json:"traderuid"`
	Testmode  bool    `json:"testmode"`
	Leverage  Decimal `json:"leverage"`
}

// struct format of request to stop a bot
type StopBotReq struct {
	UserID    string `json:"userID"`
	BotID     int    `json:"botID"`
	TraderUID string `json:"traderuid"`
}

// struct format of request to get info on remaining balances
type GetBalanceInfoReq struct {
	UserID string `json:"userID"`

	// bot locator is a map of binanceUIDs to a list of bot hashes
	BotLocator map[string][]string `json:"botLocator"`
}

// struct format of response for getting info on remaining balances
type GetBalanceInfoResp struct {
	// map of botHashes to their active balances
	BalanceData map[string]float64
	Err         string
}

// struct formats for http handler to get logs for a specific bot,
// from a specific monitor
type GetBotLogsLocalReq struct {
	BinanceID string `json:"binanceID"`
	BotHash   string `json:"botHash"`
}

type GetBotLogsLocalResp struct {
	LogsJsonStr string `json:"logsJsonStr"`
	Err         string `json:"err"`
}

type PaymentSentRequest struct {
	Plan      string `json:"planName"`
	TxID      string `json:"txID"`
	IsRenewal bool   `json:"isRenewal"`
}

type PaymentSentResponse struct {
	VerificationInProgress bool   `json:"verificationInProgress"`
	Err                    string `json:"err"`
}

func (s StartBotParam) String() string {
	return fmt.Sprintf("StartBotReq{UserID: %s, BotID: %d, Exchange: %s, ApiKey: %s, TraderUID: %s, Testmode: %t, Leverage: %s}",
		s.UserID, s.BotID, s.Exchange, s.ApiKey, s.TraderUID, s.Testmode, s.Leverage)
}

func (s StopBotReq) String() string {
	return fmt.Sprintf("StopBotReq{UserID: %s, BotID: %d, TraderUID: %s}", s.UserID, s.BotID, s.TraderUID)
}
