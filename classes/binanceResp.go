package cls

type BinanceResp struct {
	Code          string      `json:"code"`
	Message       interface{} `json:"message"`
	MessageDetail interface{} `json:"messageDetail"`
	Data          struct {
		OtherPositionRetList []struct {
			Symbol      string  `json:"symbol"`
			EntryPrice  float64 `json:"entryPrice"`
			MarkPrice   float64 `json:"markPrice"`
			Pnl         float64 `json:"pnl"`
			Roe         float64 `json:"roe"`
			Amount      float64 `json:"amount"`
			Timestamp   float64 `json:"updateTimeStamp"`
			TradeBefore bool    `json:"tradeBefore"`
			Leverage    int     `json:"leverage"`
		} `json:"otherPositionRetList"`
		Timestamp float64 `json:"updateTimeStamp"`
	} `json:"data"`
	Success bool `json:"success"`
}
