package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Cifrado en reposo AES-256-GCM con la master key (guía §9.2): nonce de 12
// bytes prefijado al ciphertext, todo en base64 estándar. Lo consumen las
// credenciales cifradas (API keys LLM, tokens GitLab, deploy keys — F1).
const nonceSize = 12

// Encrypt cifra plaintext y devuelve nonce||ciphertext en base64.
func Encrypt(key, plaintext []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generando nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plaintext, nil)), nil
}

// Decrypt descifra el formato de Encrypt. Un ciphertext alterado o una clave
// distinta fallan (GCM verifica autenticidad).
func Decrypt(key []byte, encoded string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("ciphertext no es base64 válido: %w", err)
	}
	if len(raw) < nonceSize+1 {
		return nil, errors.New("ciphertext demasiado corto")
	}
	plaintext, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("descifrado falló (clave incorrecta o dato alterado): %w", err)
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("la master key debe tener 32 bytes (AES-256), tiene %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("inicializando AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("inicializando GCM: %w", err)
	}
	return gcm, nil
}
