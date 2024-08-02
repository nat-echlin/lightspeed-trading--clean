package cls

import (
	"fmt"
	"strings"
	"time"
)

type BotPosition struct {
	Symbol          string
	Side            string
	Qty             Decimal
	Entry           Decimal
	Leverage        Decimal
	IgnoreAddsUntil time.Time
	Tp              Decimal
	Sl              Decimal
	Exchange        string
}

func (b BotPosition) String() string {
	return fmt.Sprintf("Symbol: %s, Side: %s, Qty: %s, Entry: %s, Leverage: %s, Tp: %s, Sl: %s",
		b.Symbol, b.Side, b.Qty, b.Entry, b.Leverage, b.Tp, b.Sl,
	)
}

func (b BotPosition) GetHash() string {
	return GetPosHash(b.Symbol, b.Side)
}

// side should be one of ["Buy", "Sell"]
// qty should be the new qty of the position
func GetPingDescription(side string, qty Decimal, symbol string) string {
	// get ping pingSide
	pingSide := "Long"
	if side == "Sell" {
		pingSide = "Short"
	}

	// format and return
	return fmt.Sprintf("%s %s %s", pingSide, qty, symbol)
}

// fmtFloat formats a float64 to remove unnecessary trailing zeros
func FmtFloat(fl float64) string {
	// convert float to string with sufficient precision
	str := fmt.Sprintf("%.8f", fl)

	str = strings.TrimRight(str, "0")
	str = strings.TrimSuffix(str, ".")

	return str
}
