package cls

import "fmt"

// get bot hash key used for the Monitor.bots map
func GetBotHash(userID string, botID int) string {
	return fmt.Sprintf("%s_%d", userID, botID)
}

// get hash used for positions
func GetPosHash(symbol string, side string) string {
	return side + symbol
}
