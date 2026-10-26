package crypto

import (
	"bytes"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	passphrase := "my-secret-test-passphrase-12345"
	original := []byte("SQLite database binary content simulation 1234567890!@#$%^&*()")

	ciphertext, err := Encrypt(original, passphrase)
	if err != nil {
		t.Fatalf("暗号化に失敗しました: %v", err)
	}

	if bytes.Equal(ciphertext, original) {
		t.Fatal("暗号化結果が平文と一致してしまっています")
	}

	decrypted, err := Decrypt(ciphertext, passphrase)
	if err != nil {
		t.Fatalf("復号に失敗しました: %v", err)
	}

	if !bytes.Equal(decrypted, original) {
		t.Fatalf("復号結果が元データと一致しません: got %s, want %s", string(decrypted), string(original))
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	original := []byte("secret data")
	ciphertext, err := Encrypt(original, "correct-key")
	if err != nil {
		t.Fatalf("暗号化に失敗しました: %v", err)
	}

	_, err = Decrypt(ciphertext, "wrong-key")
	if err == nil {
		t.Fatal("異なる鍵で復号できてしまいました（エラーが返るべきです）")
	}
}
