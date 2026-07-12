package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// discordUser is the subset of GET /users/@me the portal reads (scope
// `identify`). id is required; username is display metadata.
type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// authorizeURL builds the Discord consent URL the login handler redirects to.
// scope is exactly `identify` — no email, no guilds (D5). The state nonce is
// echoed back to the callback and checked against the state cookie (CSRF).
func (p *Portal) authorizeURL(state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.clientID)
	q.Set("scope", "identify")
	q.Set("state", state)
	q.Set("redirect_uri", p.redirectURI())
	// prompt=none would skip re-consent, but keeping the default lets a user
	// switch accounts; the plan does not require it either way.
	return p.discordBase + "/oauth2/authorize?" + q.Encode()
}

// exchangeCode swaps an authorization code for an access token at Discord's
// token endpoint. On any non-2xx or transport error it returns an error whose
// message NEVER includes the client secret (only the status/step), so the
// caller can log it safely.
func (p *Portal) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.redirectURI())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.discordBase+"/api/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	// Cap the body: a hostile/broken endpoint must not stream unbounded data.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do NOT echo the body — a token endpoint may reflect request params
		// (incl. the secret) in an error. Status only.
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}
	return tok.AccessToken, nil
}

// fetchUser calls GET /users/@me with the access token and returns the Discord
// identity. A missing id is an error (we cannot bind a key without it).
func (p *Portal) fetchUser(ctx context.Context, accessToken string) (discordUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.discordBase+"/api/users/@me", nil)
	if err != nil {
		return discordUser{}, fmt.Errorf("build user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return discordUser{}, fmt.Errorf("user request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return discordUser{}, fmt.Errorf("users/@me returned %d", resp.StatusCode)
	}
	var u discordUser
	if err := json.Unmarshal(body, &u); err != nil {
		return discordUser{}, fmt.Errorf("decode user response: %w", err)
	}
	if u.ID == "" {
		return discordUser{}, fmt.Errorf("users/@me missing id")
	}
	return u, nil
}
