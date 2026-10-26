package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// DeriveKey は任意の長さのパスフレーズからSHA-256を用いて32バイト（AES-256用）の暗号化鍵を生成します。
func DeriveKey(passphrase string) []byte {
	hash := sha256.Sum256([]byte(passphrase))
	return hash[:]
}

// Encrypt はプレーンテキストをAES-256-GCM方式で暗号化します。
// 出力フォーマットは [12バイト Nonce] + [暗号文および認証タグ] です。
func Encrypt(plaintext []byte, passphrase string) ([]byte, error) {
	key := DeriveKey(passphrase)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes暗号ブロックの生成に失敗しました: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcmの生成に失敗しました: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("乱数nonceの生成に失敗しました: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt は暗号化されたバイト列をAES-256-GCM方式で復号します。
func Decrypt(ciphertext []byte, passphrase string) ([]byte, error) {
	key := DeriveKey(passphrase)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes暗号ブロックの生成に失敗しました: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcmの生成に失敗しました: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("暗号化データのサイズが不正です")
	}

	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("復号に失敗しました（鍵が異なるかデータが破損しています）: %w", err)
	}

	return plaintext, nil
}
