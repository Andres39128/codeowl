package gitlab

// Verificación de firmas de webhooks GitLab (guía §9.3): Standard Webhooks
// (signing token, GA desde 19.1) con fallback legacy X-Gitlab-Token para
// self-managed viejas.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// whsecPrefix y v1Prefix son los formatos del signing token y de cada firma
// del header webhook-signature ("v1,{base64}", una o más separadas por
// espacio).
const (
	whsecPrefix = "whsec_"
	v1Prefix    = "v1,"
)

// signingKey extrae la clave cruda del HMAC: el signing token llega con el
// prefijo whsec_ — se quita y se base64-decodea (§9.3).
func signingKey(secret string) ([]byte, error) {
	raw, ok := strings.CutPrefix(secret, whsecPrefix)
	if !ok {
		return nil, errors.New("el signing token no tiene prefijo whsec_")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("la clave no es base64 válido: %w", err)
	}
	return key, nil
}

// verifyStandardWebhooks verifica la firma Standard Webhooks (§9.3): HMAC
// SHA-256 sobre "{webhook-id}.{webhook-timestamp}.{body}" con la clave cruda,
// comparación en tiempo constante contra cada firma v1,{base64} y chequeo de
// frescura del timestamp contra replay.
func verifyStandardWebhooks(secret, id, timestamp, signatureHeader string, body []byte, tolerance time.Duration) error {
	key, err := signingKey(secret)
	if err != nil {
		return err
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("webhook-timestamp no es un unix timestamp")
	}
	if d := time.Since(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("timestamp fuera de la tolerancia de %s (replay)", tolerance)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, sig := range strings.Fields(signatureHeader) {
		got, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sig, v1Prefix))
		if err != nil {
			continue // firma malformada: no es un match, se siguen probando
		}
		if hmac.Equal(want, got) {
			return nil
		}
	}
	return errors.New("ninguna firma coincide")
}

// verifyLegacyToken compara X-Gitlab-Token contra el secret token plano del
// repo (self-managed viejas, §9.3). Sin secret en el repo no hay nada que
// comparar: rechaza.
func verifyLegacyToken(secret, header string) bool {
	return secret != "" && hmac.Equal([]byte(secret), []byte(header))
}
