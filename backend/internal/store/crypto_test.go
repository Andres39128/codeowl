package store

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plaintext := []byte(`{"api_key":"sk-123"}`)

	encoded, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(key, encoded)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip: got %q want %q", got, plaintext)
	}
}

func TestEncryptNonceUnico(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	a, _ := Encrypt(key, []byte("mismo texto"))
	b, _ := Encrypt(key, []byte("mismo texto"))
	if a == b {
		t.Error("dos cifrados del mismo texto no deben coincidir (nonce aleatorio)")
	}
}

func TestDecryptRechazaAlteradoYClaveIncorrecta(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	encoded, err := Encrypt(key, []byte("secreto"))
	if err != nil {
		t.Fatal(err)
	}

	tampered := []byte(encoded)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := Decrypt(key, string(tampered)); err == nil {
		t.Error("ciphertext alterado debe fallar")
	}

	otraClave := bytes.Repeat([]byte{9}, 32)
	if _, err := Decrypt(otraClave, encoded); err == nil {
		t.Error("clave distinta debe fallar")
	}
}

func TestEncryptRechazaClaveCorta(t *testing.T) {
	if _, err := Encrypt(bytes.Repeat([]byte{1}, 16), []byte("x")); err == nil {
		t.Error("clave de 16 bytes no es AES-256: debe fallar")
	}
}
