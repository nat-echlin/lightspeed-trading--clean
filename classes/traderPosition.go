package cls

// side is "Buy" or "Sell" depending on long / short
type TraderPosition struct {
	Symbol     string
	Side       string
	Qty        float64
	Timestamp  float64
	EntryPrice float64
	Pnl        float64
	MarkPrice  float64
}

func NewPosition(quantity float64, timestamp float64, coin string, entry float64, pnl float64, mark float64) TraderPosition {
	pos := TraderPosition{
		coin,
		getSide(quantity),
		quantity,
		timestamp,
		entry,
		pnl,
		mark,
	}
	return pos
}

func getSide(qty float64) string {
	switch {
	case qty > 0:
		return "Buy"
	case qty < 0:
		return "Sell"
	default:
		return "Buy"
	}
}
