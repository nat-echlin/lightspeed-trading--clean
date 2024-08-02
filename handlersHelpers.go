package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/lightspeed-trading/app/common"
)

// send any interface that can be marshalled as an http response.
func sendStructToUser(v any, w http.ResponseWriter, code int) {
	w.WriteHeader(code)

	// marshall struct as json
	jsonData, err := json.Marshal(v)
	if err != nil {
		log.Printf("err marshalling json, %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// send to user
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonData)

	if code == 500 {
		go common.SendStaffAlert("Sent 500 response to user, check logs.")
	}
}

// check if a webhook is valid
func webhookIsOK(webhook string) bool {
	validPrefixes := []string{
		"https://discord.com/api/webhooks/",
		"https://canary.discord.com/api/webhooks/",
	}

	for _, validPrefix := range validPrefixes {
		if strings.HasPrefix(webhook, validPrefix) {
			return true
		}
	}

	return false
}
