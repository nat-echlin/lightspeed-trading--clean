package db

import "fmt"

type SignalConfig struct {
	UserID    string `json:"userID,omitempty"`
	BinanceID string `json:"binanceID,omitempty"`
	TargetURL string `json:"targetURL,omitempty"`
}

func (sc SignalConfig) String() string {
	return fmt.Sprintf("SignalConfig{UserID: %s, BinanceID: %s, TargetURL: %s}", sc.UserID, sc.BinanceID, sc.TargetURL)
}
