package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
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

// GDriveAuthURL generates a Google OAuth2 consent URL.
//
//	POST /api/v1/storage/gdrive/auth-url
func (h *StorageHandler) GDriveAuthURL(w http.ResponseWriter, r *http.Request) {
	var req gdriveAuthURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ClientID == "" || req.ClientSecret == "" || req.RedirectURI == "" {
		respondError(w, http.StatusBadRequest, "client_id, client_secret, and redirect_uri are required")
		return
	}

	cfg := &oauth2.Config{
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
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
//
//	POST /api/v1/storage/gdrive/exchange-token
func (h *StorageHandler) GDriveExchangeToken(w http.ResponseWriter, r *http.Request) {
	var req gdriveExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ClientID == "" || req.ClientSecret == "" || req.Code == "" || req.RedirectURI == "" {
		respondError(w, http.StatusBadRequest, "client_id, client_secret, code, and redirect_uri are required")
		return
	}

	cfg := &oauth2.Config{
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
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

// generateOAuthState returns a cryptographically random state string for CSRF protection.
func generateOAuthState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
