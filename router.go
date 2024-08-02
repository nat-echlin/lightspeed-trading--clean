package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"
	cls "github.com/lightspeed-trading/app/classes"
)

func CreateServer(
	dbConnPool *pgxpool.Pool,
	adminApiKey string,
	dbEncryptionKey string,
	ourWalletAddress string,
	activePayments *cls.PaymentsHdlr,
	ethClient *ethclient.Client,
	v *verifier,
	lmc *cls.LaunchingMonitorsCache,
) *http.Server {
	router := http.NewServeMux()

	// admin specific endpoints
	router.Handle("/admin/addPlanToUser", corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onAdminAddPlanToUser(w, r, dbConnPool, adminApiKey)
	})))
	router.Handle("/admin/createPlan", corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onAdminCreateNewPlan(w, r, dbConnPool, adminApiKey)
	})))
	router.Handle("/admin/deletePlan", corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onAdminDeletePlan(w, r, dbConnPool, adminApiKey)
	})))

	// needs auth
	router.Handle("/user/listBots", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		OnListBots(w, r, dbConnPool, dbEncryptionKey)
	}))))
	router.Handle("/user/updateSettings", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onUpdateSettings(w, r, dbConnPool, dbEncryptionKey, false, lmc)
	}))))
	router.Handle("/user/getLicense", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onGetLicense(w, r, dbConnPool)
	}))))
	router.Handle("/payment/sent", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onPaymentSent(w, r, ourWalletAddress, activePayments, dbConnPool, ethClient)
	}))))
	router.Handle("/payment/status", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onCheckPaymentStatus(w, r, activePayments)
	}))))
	router.Handle("/user/bot/start", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onStartBot(w, r, dbConnPool, lmc, dbEncryptionKey)
	}))))
	router.Handle("/user/bot/stop", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onStopBot(w, r, dbConnPool)
	}))))
	router.Handle("/user/setWebhook", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onSetUserAlertWebhook(w, r, dbConnPool)
	}))))
	router.Handle("/user/getLogs", corsMiddleware(v.authoriseJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onGetLogs(w, r, dbConnPool)
	}))))

	// no auth
	router.Handle("/plans/get", corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}
		onGetPlans(w, r, dbConnPool)
	})))

	server := &http.Server{
		Addr:     PORT,
		Handler:  router,
		ErrorLog: log.New(&errorLogWriter{}, "", 0), // custom error logger
	}

	return server
}

type errorLogWriter struct{}

func (elw *errorLogWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if strings.HasSuffix(msg, "tls: first record does not look like a TLS handshake") {
		return len(p), nil // Suppress this specific log message
	}
	return os.Stdout.Write(p) // Log other messages
}
