package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cliente HTTP mínimo para APIs chat-completions OpenAI-compatible (mapa:
// backend.llm). OpenAI, ZhipuAI y Ollama exponen el mismo formato en
// {base_url}/v1/chat/completions — un solo código sirve para cualquiera.

// maxErrorBody acota cuánto cuerpo de error se lee antes de descartarlo:
// basta para el mensaje del proveedor, evita leer respuestas enormes.
const maxErrorBody = 4096

// maxRetryAfter acota el header Retry-After: un proveedor con un valor
// patológico no puede retener un slot del gateway indefinidamente.
const maxRetryAfter = 60 * time.Second

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// MaxTokens cubre razonamiento + respuesta: los modelos de razonamiento
	// (MiniMax M3 y familia) queman el presupuesto del proveedor pensando y
	// devuelven content vacío si el default implícito es chico. omitempty
	// conserva el comportamiento previo cuando el gateway no lo configura.
	MaxTokens int `json:"max_tokens,omitempty"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage usage `json:"usage"`
}

// statusError es una respuesta no-200 del proveedor. Retryable marca 429/5xx
// (§9.7: backoff y reintento acotado); los demás códigos (400/401/403 —
// errores del cliente) conmutan de proveedor sin reintentar. retryAfter es 0
// si no hay header válido → el caller cae al backoff exponencial.
type statusError struct {
	code       int
	body       string
	retryable  bool
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return fmt.Sprintf("el proveedor respondió %d: %s", e.code, e.body)
}

func newStatusError(code int, body []byte, retryAfter string) *statusError {
	retryable := code == http.StatusTooManyRequests || code >= 500
	se := &statusError{
		code:      code,
		body:      string(bytes.TrimSpace(body)),
		retryable: retryable,
	}
	if retryable {
		se.retryAfter = parseRetryAfter(retryAfter)
	}
	return se
}

// parseRetryAfter interpreta el header Retry-After en forma de segundos
// (la forma HTTP-date es rara en APIs de LLM y cae al backoff exponencial).
// Devuelve 0 si el header está ausente o es inválido.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// chatCompletion hace un POST {baseURL}/v1/chat/completions con Bearer
// api_key y extrae content de choices[0].message.content y los tokens de
// usage. El timeout del intento lo fija el caller vía ctx.
func chatCompletion(ctx context.Context, client *http.Client, baseURL, apiKey, model, systemPrompt, userPrompt string, maxTokens int) (chatResponse, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return chatResponse{}, fmt.Errorf("serializando request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return chatResponse{}, fmt.Errorf("armado del request a %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return chatResponse{}, fmt.Errorf("llamando a %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return chatResponse{}, newStatusError(resp.StatusCode, body, resp.Header.Get("Retry-After"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatResponse{}, fmt.Errorf("leyendo respuesta de %s: %w", url, err)
	}
	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return chatResponse{}, fmt.Errorf("parseando respuesta de %s: %w", url, err)
	}
	if len(out.Choices) == 0 {
		return chatResponse{}, fmt.Errorf("respuesta de %s sin choices", url)
	}
	return out, nil
}

// responseError es una respuesta 200 estructuralmente inválida del proveedor
// (JSON malformado, conteo/índices/dims inconsistentes). No es retryable: el
// mismo proveedor devolvería lo mismo — conmuta directo al siguiente.
type responseError struct{ msg string }

func (e *responseError) Error() string { return e.msg }

func newResponseError(format string, args ...any) *responseError {
	return &responseError{msg: fmt.Sprintf(format, args...)}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage usage `json:"usage"`
}

// embeddings hace un POST {baseURL}/v1/embeddings con Bearer api_key —
// mismo formato OpenAI-compatible que el chat (§6 F4) — y valida la
// respuesta: un dato por input rearmado por index (los proveedores pueden
// responder desordenado), cada embedding no vacío y dims consistentes en el
// lote. Una respuesta 200 que no cumple sale como responseError. El timeout
// del intento lo fija el caller vía ctx.
func embeddings(ctx context.Context, client *http.Client, baseURL, apiKey, model string, texts []string) (embedResult, error) {
	reqBody, err := json.Marshal(embedRequest{Model: model, Input: texts})
	if err != nil {
		return embedResult{}, fmt.Errorf("serializando request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return embedResult{}, fmt.Errorf("armado del request a %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return embedResult{}, fmt.Errorf("llamando a %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return embedResult{}, newStatusError(resp.StatusCode, body, resp.Header.Get("Retry-After"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return embedResult{}, fmt.Errorf("leyendo respuesta de %s: %w", url, err)
	}
	var out embedResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return embedResult{}, newResponseError("respuesta malformada de %s: %v", url, err)
	}
	if len(out.Data) != len(texts) {
		return embedResult{}, newResponseError("respuesta de %s con %d embeddings para %d inputs", url, len(out.Data), len(texts))
	}

	vecs := make([][]float32, len(texts))
	var dims int
	for _, d := range out.Data {
		switch {
		case d.Index < 0 || d.Index >= len(texts):
			return embedResult{}, newResponseError("respuesta de %s con index %d fuera de rango", url, d.Index)
		case len(d.Embedding) == 0:
			return embedResult{}, newResponseError("respuesta de %s con embedding vacío en index %d", url, d.Index)
		case dims == 0:
			dims = len(d.Embedding)
		case len(d.Embedding) != dims:
			return embedResult{}, newResponseError("respuesta de %s con dims inconsistentes: %d vs %d", url, len(d.Embedding), dims)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		vecs[d.Index] = vec
	}
	for i, v := range vecs {
		if v == nil {
			// Un proveedor que no setea index deja huecos: todos los datos
			// caerían en 0 y el resto del lote queda nil.
			return embedResult{}, newResponseError("respuesta de %s sin dato para el index %d", url, i)
		}
	}
	return embedResult{vecs: vecs, usage: out.Usage}, nil
}
