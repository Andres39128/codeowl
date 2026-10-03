// Tests del agente Chat con stubs (§4.5: stubs de pocas líneas, sin BD ni
// mocks): comando /review abierto/cerrado, /tests, /explain, chat general e
// idempotencia por comentario padre (§9.12). Reusa stubStore/stubGateway/
// stubVCS de review_test.go (mismo paquete).
package review

import (
	"context"
	"strings"
	"testing"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// chatParent es el comentario padre canónico de los tests de chat.
const chatParent = "999"

// chatInput es la entrada canónica: mención general sobre el PR abierto del
// stub.
func chatInput() ChatInput {
	return ChatInput{
		PullRequestID:   testPRID,
		RepositoryID:    testRepoID,
		ParentCommentID: chatParent,
		CommentBody:     "@codeowl-bot ¿qué cambia esto?",
		CommentAuthor:   "dev",
		HeadSHA:         testHead,
		BaseSHA:         testBase,
		Language:        "es",
	}
}

// chatRows devuelve las filas de chat registradas en el stub.
func chatRows(d *deps) []store.CommentsSent {
	return d.st.comments["chat"]
}

// /review sobre un PR abierto: EnqueueReview=true para el worker, sin LLM,
// sin respuesta en el hilo y sin fila de chat (mapa: /review no responde).
func TestChatReviewEnPRAbiertoEncola(t *testing.T) {
	d := newDeps()
	d.gw.chat = []string{"el chat no debería ser llamado"}

	in := chatInput()
	in.CommentBody = "@codeowl-bot /review"
	res, err := HandleChat(context.Background(), DefaultChatConfig(), d.st, d.gw, d.vcs, in)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	if !res.EnqueueReview {
		t.Error("/review sobre un PR abierto debe encolar el ReviewJob")
	}
	if d.gw.calls != 0 {
		t.Errorf("/review no llama LLM: llamadas=%d", d.gw.calls)
	}
	if len(d.vcs.replyBods) != 0 {
		t.Errorf("/review no responde en el hilo: publicó %d", len(d.vcs.replyBods))
	}
	if len(chatRows(d)) != 0 {
		t.Errorf("/review no registra fila de chat: %d", len(chatRows(d)))
	}
}

// /review sobre un PR cerrado: responde la negativa sin encolar (§3.6) y
// registra la fila — el reintento no la duplica.
func TestChatReviewEnPRCerradoRespondeSinEncolar(t *testing.T) {
	d := newDeps()
	d.st.pr.State = "closed"

	in := chatInput()
	in.CommentBody = "@codeowl-bot /review"
	res, err := HandleChat(context.Background(), DefaultChatConfig(), d.st, d.gw, d.vcs, in)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	if res.EnqueueReview {
		t.Error("/review sobre un PR cerrado no debe encolar (§3.6)")
	}
	if !strings.Contains(res.Response, "cerrado") {
		t.Errorf("la negativa debe decir que está cerrado: %q", res.Response)
	}
	if len(d.vcs.replyBods) != 1 {
		t.Fatalf("la negativa se publica en el hilo: publicados=%d", len(d.vcs.replyBods))
	}
	if d.gw.calls != 0 {
		t.Errorf("la negativa no llama LLM: llamadas=%d", d.gw.calls)
	}
	rows := chatRows(d)
	if len(rows) != 1 || !rows[0].ParentCommentID.Valid || rows[0].ParentCommentID.String != chatParent {
		t.Fatalf("la negativa debe registrarse con su padre: %+v", rows)
	}

	// Reintento del ChatJob: la fila corta — sin segunda publicación.
	res2, err := HandleChat(context.Background(), DefaultChatConfig(), d.st, d.gw, d.vcs, in)
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if res2.EnqueueReview || len(d.vcs.replyBods) != 1 {
		t.Errorf("el reintento no debe republicar ni encolar: encola=%v publicados=%d",
			res2.EnqueueReview, len(d.vcs.replyBods))
	}
}

// /tests: el LLM genera sugerencias con el framework detectado del diff
// (.go → go test) y la respuesta se publica en el hilo del comentario.
func TestChatTestsGeneraSugerencias(t *testing.T) {
	d := newDeps()
	d.gw.chat = []string{"Sugerencia: test de tabla para fmt.Println."}

	in := chatInput()
	in.CommentBody = "@codeowl-bot /tests por favor"
	res, err := HandleChat(context.Background(), DefaultChatConfig(), d.st, d.gw, d.vcs, in)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	if res.Response != "Sugerencia: test de tabla para fmt.Println." {
		t.Errorf("respuesta: %q", res.Response)
	}
	if d.gw.calls != 1 {
		t.Fatalf("/tests es una llamada LLM: llamadas=%d", d.gw.calls)
	}
	prompt := d.gw.chatSeen[0]
	if !strings.Contains(prompt, "go test") {
		t.Errorf("el prompt debe nombrar el framework detectado (.go → go test): %q", prompt)
	}
	if !strings.Contains(prompt, "main.go") {
		t.Errorf("el prompt debe traer el diff: %q", prompt)
	}
	if len(d.vcs.replyParents) != 1 || d.vcs.replyParents[0] != chatParent {
		t.Errorf("la respuesta va al hilo del comentario padre: %v", d.vcs.replyParents)
	}
	rows := chatRows(d)
	if len(rows) != 1 || rows[0].ParentCommentID.String != chatParent {
		t.Errorf("la respuesta debe registrarse con su padre: %+v", rows)
	}
}

// /explain: el diff se explica en lenguaje llano vía LLM.
func TestChatExplainExplicaDiff(t *testing.T) {
	d := newDeps()
	d.gw.chat = []string{"El diff agrega un import y cambia un saludo."}

	in := chatInput()
	in.CommentBody = "@codeowl-bot /explain"
	res, err := HandleChat(context.Background(), DefaultChatConfig(), d.st, d.gw, d.vcs, in)
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	if !strings.Contains(res.Response, "saludo") {
		t.Errorf("respuesta: %q", res.Response)
	}
	if d.gw.calls != 1 {
		t.Fatalf("/explain es una llamada LLM: llamadas=%d", d.gw.calls)
	}
	if !strings.Contains(d.gw.chatSeen[0], "explicá el diff") {
		t.Errorf("el prompt debe pedir la explicación: %q", d.gw.chatSeen[0])
	}
	if len(d.vcs.replyBods) != 1 {
		t.Errorf("la explicación se publica en el hilo: %d", len(d.vcs.replyBods))
	}
}

// Mención general (pregunta sin comando): chat con el diff como contexto.
// De paso, el tope de diff del chat (§9.6) recorta y declara el corte.
func TestChatMencionGeneralResponde(t *testing.T) {
	d := newDeps()
	d.gw.chat = []string{"Agrega un import de fmt."}
	cfg := DefaultChatConfig()
	cfg.DiffMaxLines = 5 // diff fixture tiene más de 5 líneas: recorta

	res, err := HandleChat(context.Background(), cfg, d.st, d.gw, d.vcs, chatInput())
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	if res.Response != "Agrega un import de fmt." {
		t.Errorf("respuesta: %q", res.Response)
	}
	prompt := d.gw.chatSeen[0]
	if !strings.Contains(prompt, "¿qué cambia esto?") {
		t.Errorf("el prompt debe traer la pregunta del usuario: %q", prompt)
	}
	if !strings.Contains(prompt, "truncado") {
		t.Errorf("el recorte se declara en el prompt (§9.6): %q", prompt)
	}
	if len(d.vcs.replyBods) != 1 {
		t.Errorf("la respuesta se publica en el hilo: %d", len(d.vcs.replyBods))
	}
}

// Idempotencia (§9.12): una fila de chat previa para el mismo comentario
// padre corta sin LLM, sin publicación y sin encolar — el reintento del
// ChatJob no duplica la respuesta.
func TestChatReintentoIdempotente(t *testing.T) {
	d := newDeps()
	if _, err := d.st.CreateCommentSent(context.Background(), store.CreateCommentSentParams{
		PullRequestID:   testPRID,
		CommentID:       "chat-viejo",
		Type:            "chat",
		ParentCommentID: pgText(chatParent),
	}); err != nil {
		t.Fatalf("seed de comments_sent: %v", err)
	}
	d.gw.chat = []string{"el chat no debería ser llamado"}
	d.vcs.diff = diffFixture

	res, err := HandleChat(context.Background(), DefaultChatConfig(), d.st, d.gw, d.vcs, chatInput())
	if err != nil {
		t.Fatalf("HandleChat: %v", err)
	}
	if res.Response != "" || res.EnqueueReview {
		t.Errorf("el reintento no hace nada: %+v", res)
	}
	if d.gw.calls != 0 {
		t.Errorf("el reintento no llama LLM: llamadas=%d", d.gw.calls)
	}
	if len(d.vcs.replyBods) != 0 {
		t.Errorf("el reintento no publica: %d", len(d.vcs.replyBods))
	}
}
