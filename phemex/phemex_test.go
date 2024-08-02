package phemex

import (
	"log"
	"testing"

	"github.com/joho/godotenv"
)

var TESTNET bool = true

func TestLogin(t *testing.T) {
	// get bal to test login status
	cli := getClient()
	resp, err := cli.GetBalance("USDT")
	if err != nil {
		t.Fatalf("failed to get balance, %v", err)
	}

	t.Fatal(resp)
}

func getClient() *Client {
	var creds map[string]string
	creds, err := godotenv.Read("phemexKeys.env")
	if err != nil {
		log.Fatalf("failed to read env file, %v", err)
	}

	keyEnvName := "ADMIN_PHEMEX_API_KEY"
	secretEnvName := "ADMIN_PHEMEX_API_SECRET"
	if TESTNET {
		keyEnvName += "_TEST"
		secretEnvName += "_TEST"
	}

	cli := NewClient(TESTNET, creds[keyEnvName], creds[secretEnvName])
	return cli
}

func TestGetPositions(t *testing.T) {
	// cli := getClient()

}
