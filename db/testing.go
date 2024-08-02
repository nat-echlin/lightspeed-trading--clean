package db

import (
	"fmt"
	"reflect"
)

func CheckBotConfigEquality(bc1 BotConfig, bc2 BotConfig, encryptionKey string) error {
	decryptedKey1, err := Decrypt(bc1.APIKeyRaw, encryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt key 1, %v", err)
	}
	decryptedSecret1, err := Decrypt(bc1.APISecretRaw, encryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt secret 1, %v", err)
	}

	decryptedKey2, err := Decrypt(bc2.APIKeyRaw, encryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt key 2, %v", err)
	}
	decryptedSecret2, err := Decrypt(bc2.APISecretRaw, encryptionKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt secret 2, %v", err)
	}

	bc1.APIKeyRaw = nil
	bc1.APISecretRaw = nil
	bc2.APIKeyRaw = nil
	bc2.APISecretRaw = nil

	if !reflect.DeepEqual(bc1, bc2) {
		return fmt.Errorf("configs do not match.\nbc1: %+v\nbc2: %+v", bc1, bc2)
	}

	if decryptedKey1 != decryptedKey2 {
		return fmt.Errorf("keys do not match, got 1: %s and 2: %s", decryptedKey1, decryptedKey2)
	}

	if decryptedSecret1 != decryptedSecret2 {
		return fmt.Errorf("secrets do not match, got 1: %s and 2: %s", decryptedSecret1, decryptedSecret2)
	}

	return nil
}
