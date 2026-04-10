package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
)

// Environment variable names for embedded Google Drive OAuth credentials.
// When set, users can connect to Google Drive without providing their own
// Cloud Console credentials.
const (
	envGDriveClientID     = "VAULT_GDRIVE_CLIENT_ID"
	envGDriveClientSecret = "VAULT_GDRIVE_CLIENT_SECRET" //nolint:gosec // env var name, not a credential
)

type gdriveAuthURLRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURI  string `json:"redirect_uri"`
}

type gdriveExchangeRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
}

// resolveGDriveCredentials returns the effective client_id and client_secret,
// falling back to environment variables when the request values are empty.
func resolveGDriveCredentials(clientID, clientSecret string) (string, string) {
	if clientID == "" {
		clientID = os.Getenv(envGDriveClientID)
	}
	if clientSecret == "" {
		clientSecret = os.Getenv(envGDriveClientSecret)
	}
	return clientID, clientSecret
}

// GDriveStatus reports whether embedded Google Drive OAuth credentials are
// configured, allowing users to connect without their own Cloud Console credentials.
//
//	GET /api/v1/replication/gdrive/status
func (h *ReplicationHandler) GDriveStatus(w http.ResponseWriter, _ *http.Request) {
	id, secret := resolveGDriveCredentials("", "")
	respondJSON(w, http.StatusOK, map[string]bool{
		"configured": id != "" && secret != "",
	})
}

// GDriveAuthURL generates a Google OAuth2 consent URL.
// When client_id and client_secret are omitted the server falls back to
// environment-provided defaults (VAULT_GDRIVE_CLIENT_ID / _SECRET).
//
//	POST /api/v1/replication/gdrive/auth-url
func (h *ReplicationHandler) GDriveAuthURL(w http.ResponseWriter, r *http.Request) {
	var req gdriveAuthURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RedirectURI == "" {
		respondError(w, http.StatusBadRequest, "redirect_uri is required")
		return
	}

	clientID, clientSecret := resolveGDriveCredentials(req.ClientID, req.ClientSecret)
	if clientID == "" || clientSecret == "" {
		respondError(w, http.StatusBadRequest,
			"Google Drive is not configured. Set VAULT_GDRIVE_CLIENT_ID and VAULT_GDRIVE_CLIENT_SECRET environment variables, or provide client_id and client_secret.")
		return
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  req.RedirectURI,
		Scopes:       []string{drive.DriveFileScope},
		Endpoint:     google.Endpoint,
	}

	state, err := generateOAuthState()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to generate state")
		return
	}

	url := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	respondJSON(w, http.StatusOK, map[string]string{"url": url, "state": state})
}

// GDriveExchangeToken exchanges an OAuth2 authorisation code for a refresh token.
// When client_id and client_secret are omitted the server falls back to
// environment-provided defaults.
//
//	POST /api/v1/replication/gdrive/exchange-token
func (h *ReplicationHandler) GDriveExchangeToken(w http.ResponseWriter, r *http.Request) {
	var req gdriveExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Code == "" || req.RedirectURI == "" {
		respondError(w, http.StatusBadRequest, "code and redirect_uri are required")
		return
	}

	clientID, clientSecret := resolveGDriveCredentials(req.ClientID, req.ClientSecret)
	if clientID == "" || clientSecret == "" {
		respondError(w, http.StatusBadRequest, "Google Drive credentials not available")
		return
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  req.RedirectURI,
		Scopes:       []string{drive.DriveFileScope},
		Endpoint:     google.Endpoint,
	}

	token, err := cfg.Exchange(context.Background(), req.Code)
	if err != nil {
		log.Printf("gdrive token exchange failed: %v", err)
		respondError(w, http.StatusBadRequest, "token exchange failed")
		return
	}

	if token.RefreshToken == "" {
		respondError(w, http.StatusBadRequest, "no refresh token received; try re-authorising with prompt=consent")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"refresh_token": token.RefreshToken})
}

// GDriveCallback handles the OAuth2 redirect from Google after the user grants
// consent. It renders a small HTML page that posts the authorisation code back
// to the opener window via postMessage, then closes itself.
//
//	GET /api/v1/replication/gdrive/callback
func (h *ReplicationHandler) GDriveCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	errParam := r.URL.Query().Get("error")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Derive the expected origin from the request
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	expectedOrigin := scheme + "://" + host

	data := gdriveCallbackData{Code: code, Error: errParam, ExpectedOrigin: expectedOrigin}
	if errParam != "" {
		data.Success = false
	} else if code == "" {
		data.Success = false
		data.Error = "no authorisation code received"
	} else {
		data.Success = true
	}

	if err := gdriveCallbackTmpl.Execute(w, data); err != nil {
		log.Printf("gdrive callback template error: %v", err)
	}
}

type gdriveCallbackData struct {
	Success        bool
	Code           string
	Error          string
	ExpectedOrigin string
}

var gdriveCallbackTmpl = template.Must(template.New("gdrive-callback").Parse(`<!DOCTYPE html>
<html><head><title>Vault - Google Drive</title></head>
<body style="font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0">
<div style="text-align:center">
{{if .Success}}
  <h2 style="color:#22c55e">&#10003; Authorization Successful</h2>
  <p>Connecting to Google Drive...</p>
  <p style="color:#888">This window will close automatically.</p>
{{else}}
  <h2 style="color:#ef4444">Authorization Failed</h2>
  <p>{{.Error}}</p>
  <p style="color:#888">You can close this window.</p>
{{end}}
</div>
<script>
(function(){
{{if .Success}}
  if(window.opener){
    window.opener.postMessage({type:'gdrive-auth-code',code:{{.Code}}},{{.ExpectedOrigin}});
    setTimeout(function(){window.close()},2000);
  }
{{else}}
  if(window.opener){
    window.opener.postMessage({type:'gdrive-auth-error',error:{{.Error}}},{{.ExpectedOrigin}});
  }
  setTimeout(function(){window.close()},3000);
{{end}}
})();
</script>
</body></html>`))

// generateOAuthState returns a cryptographically random state string for CSRF protection.
func generateOAuthState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
