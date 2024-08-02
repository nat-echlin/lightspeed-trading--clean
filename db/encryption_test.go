package db

import (
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	passphrase := "j0mFtu2293nfAbc7"
	msg := "123456778"

	encrypted, err := Encrypt(msg, passphrase)
	if err != nil {
		t.Fatalf("failed to encrypt, %v", err)
	}

	decrypted, err := Decrypt(encrypted, passphrase)
	if err != nil {
		t.Fatalf("failed to decrypt, %v", err)
	}

	if decrypted != msg {
		t.Fatalf("decrypted (%s) does not match initial msg (%s)", decrypted, msg)
	}
}
