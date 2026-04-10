package handlers

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"

	"golang.org/x/oauth2"
)

// Environment variable names for embedded OneDrive OAuth credentials.
const (
	envOneDriveClientID     = "VAULT_ONEDRIVE_CLIENT_ID"
	envOneDriveClientSecret = "VAULT_ONEDRIVE_CLIENT_SECRET" //nolint:gosec // env var name, not a credential
)

// Microsoft identity platform endpoints for personal (consumer) accounts.
var msOneDriveEndpoint = oauth2.Endpoint{ //nolint:gosec // OAuth endpoint URLs, not credentials
	AuthURL:  "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
	TokenURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
}

type onedriveAuthURLRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURI  string `json:"redirect_uri"`
}

type onedriveExchangeRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
}

// resolveOneDriveCredentials returns the effective client_id and client_secret,
// falling back to environment variables when the request values are empty.
func resolveOneDriveCredentials(clientID, clientSecret string) (string, string) {
	if clientID == "" {
		clientID = os.Getenv(envOneDriveClientID)
	}
	if clientSecret == "" {
		clientSecret = os.Getenv(envOneDriveClientSecret)
	}
	return clientID, clientSecret
}

// OneDriveStatus reports whether embedded OneDrive OAuth credentials are
// configured.
//
//	GET /api/v1/replication/onedrive/status
func (h *ReplicationHandler) OneDriveStatus(w http.ResponseWriter, _ *http.Request) {
	id, secret := resolveOneDriveCredentials("", "")
	respondJSON(w, http.StatusOK, map[string]bool{
		"configured": id != "" && secret != "",
	})
}

// OneDriveAuthURL generates a Microsoft OAuth2 consent URL.
//
//	POST /api/v1/replication/onedrive/auth-url
func (h *ReplicationHandler) OneDriveAuthURL(w http.ResponseWriter, r *http.Request) {
	var req onedriveAuthURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.RedirectURI == "" {
		respondError(w, http.StatusBadRequest, "redirect_uri is required")
		return
	}

	clientID, clientSecret := resolveOneDriveCredentials(req.ClientID, req.ClientSecret)
	if clientID == "" || clientSecret == "" {
		respondError(w, http.StatusBadRequest,
			"OneDrive is not configured. Set VAULT_ONEDRIVE_CLIENT_ID and VAULT_ONEDRIVE_CLIENT_SECRET environment variables, or provide client_id and client_secret.")
		return
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  req.RedirectURI,
		Scopes:       []string{"Files.ReadWrite.All", "offline_access"},
		Endpoint:     msOneDriveEndpoint,
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

// OneDriveExchangeToken exchanges an OAuth2 authorisation code for a refresh token.
//
//	POST /api/v1/replication/onedrive/exchange-token
func (h *ReplicationHandler) OneDriveExchangeToken(w http.ResponseWriter, r *http.Request) {
	var req onedriveExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Code == "" || req.RedirectURI == "" {
		respondError(w, http.StatusBadRequest, "code and redirect_uri are required")
		return
	}

	clientID, clientSecret := resolveOneDriveCredentials(req.ClientID, req.ClientSecret)
	if clientID == "" || clientSecret == "" {
		respondError(w, http.StatusBadRequest, "OneDrive credentials not available")
		return
	}

	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  req.RedirectURI,
		Scopes:       []string{"Files.ReadWrite.All", "offline_access"},
		Endpoint:     msOneDriveEndpoint,
	}

	token, err := cfg.Exchange(context.Background(), req.Code)
	if err != nil {
		log.Printf("onedrive token exchange failed: %v", err)
		respondError(w, http.StatusBadRequest, "token exchange failed")
		return
	}

	if token.RefreshToken == "" {
		respondError(w, http.StatusBadRequest, "no refresh token received; try re-authorising with prompt=consent")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"refresh_token": token.RefreshToken})
}

// OneDriveCallback handles the OAuth2 redirect from Microsoft after the user
// grants consent. It renders a small HTML page that posts the authorisation
// code back to the opener window via postMessage, then closes itself.
//
//	GET /api/v1/replication/onedrive/callback
func (h *ReplicationHandler) OneDriveCallback(w http.ResponseWriter, r *http.Request) {
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

	data := onedriveCallbackData{Code: code, Error: errParam, ExpectedOrigin: expectedOrigin}
	if errParam != "" {
		data.Success = false
	} else if code == "" {
		data.Success = false
		data.Error = "no authorisation code received"
	} else {
		data.Success = true
	}

	if err := onedriveCallbackTmpl.Execute(w, data); err != nil {
		log.Printf("onedrive callback template error: %v", err)
	}
}

type onedriveCallbackData struct {
	Success        bool
	Code           string
	Error          string
	ExpectedOrigin string
}

var onedriveCallbackTmpl = template.Must(template.New("onedrive-callback").Parse(`<!DOCTYPE html>
<html><head><title>Vault - OneDrive</title></head>
<body style="font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0">
<div style="text-align:center">
{{if .Success}}
  <h2 style="color:#22c55e">&#10003; Authorization Successful</h2>
  <p>Connecting to OneDrive...</p>
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
    window.opener.postMessage({type:'onedrive-auth-code',code:{{.Code}}},{{.ExpectedOrigin}});
    setTimeout(function(){window.close()},2000);
  }
{{else}}
  if(window.opener){
    window.opener.postMessage({type:'onedrive-auth-error',error:{{.Error}}},{{.ExpectedOrigin}});
  }
  setTimeout(function(){window.close()},3000);
{{end}}
})();
</script>
</body></html>`))
