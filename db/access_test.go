package db

import (
	"context"
	"log"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

var creds map[string]string
var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	// read settings env
	envPath := "settings.env"
	var err error
	creds, err = godotenv.Read(envPath)
	if err != nil {
		log.Fatalf("failed to read env file, %v", err)
	}

	// set up pool
	pool, err = GetConnPool(creds["DB_NAME"], creds["DB_USER"], creds["DB_PASS"], true)
	if err != nil {
		log.Fatalf("failed to acquire pool, %v", err)
	}

	// run tests
	testResult := m.Run()
	os.Exit(testResult)
}

func TestWriteAndReadUsers(t *testing.T) {
	userID := "userid1123123123123123"

	// truncate
	pool.Exec(
		context.Background(),
		"TRUNCATE TABLE users;",
	)

	// write
	user := User{
		UserID:       userID,
		Plan:         "copytradeTier3",
		RenewalTS:    time.Time{},
		AlertWebhook: "",
	}

	err := WriteRecordUsers(user, pool)
	if err != nil {
		t.Fatalf("failed to write, %v", err)
	}

	// read
	readUser, err := ReadRecordUsers(userID, pool)
	if err != nil {
		t.Fatalf("failed to read user, %v", err)
	}

	if !reflect.DeepEqual(user, readUser) {
		t.Fatalf(
			`expected != got\n
			got:      %s\n
			expected: %s
			`, readUser, user,
		)
	}
}
