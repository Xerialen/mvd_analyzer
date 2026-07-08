package portal

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

// templateFS holds the portal's HTML templates and CSS. Embedded so the binary
// is self-contained (no runtime asset path, no external CDN/fonts — the portal
// runs behind the same strict environment as the API).
//
//go:embed templates/*.html
var templateFS embed.FS

// templates are parsed once at package init. base.html defines the shell;
// each page defines the "content" block. html/template auto-escapes every
// interpolation, so the attacker-influenced Discord username cannot inject
// markup — do NOT bypass this by concatenating HTML anywhere.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// pageData is the union of fields any portal page may read. Unused fields stay
// zero. All string fields are auto-escaped by html/template on render.
type pageData struct {
	DiscordName string // signed-in user's Discord username (attacker-influenced)
	HasKey      bool   // user already has an active key
	HashPrefix  string // short non-secret key identifier
	Created     string // key creation timestamp (RFC3339)
	FullKey     string // the plaintext key — set ONLY on the issued page, shown once
	Message     string // client-error page message
}

// render executes a page template into a buffer first, so a template error
// yields a clean 500 instead of a half-written 200. Content-Type is text/html.
func (p *Portal) render(w http.ResponseWriter, name string, data pageData) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		// Template bugs are server bugs; log and show a generic page.
		p.logger.Error("portal template", "template", name, "err", err.Error())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Portal pages carry secrets (the issued key) and per-user state; never let
	// a shared cache store them.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// clientError renders the error page with a caller-supplied user-facing message
// and the given 4xx status. The message must never contain a secret or an
// internal detail — it is shown verbatim (escaped) to the user.
func (p *Portal) clientError(w http.ResponseWriter, status int, msg string) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "error.html", pageData{Message: msg}); err != nil {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// serverError logs the real cause (server-side only) and shows the user a
// generic 502-class page. The err is NEVER rendered — it may reference the
// Discord endpoint or wrap a status, and must not leak. The client secret can
// never reach here because exchangeCode/fetchUser scrub it from their errors,
// but we still never render err as defence in depth.
func (p *Portal) serverError(w http.ResponseWriter, r *http.Request, step string, err error) {
	p.logger.Error("portal", "step", step, "err", err.Error(), "path", r.URL.Path)
	var buf bytes.Buffer
	msg := "Sign-in failed talking to Discord. Please try again."
	if err := templates.ExecuteTemplate(&buf, "error.html", pageData{Message: msg}); err != nil {
		http.Error(w, msg, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write(buf.Bytes())
}
