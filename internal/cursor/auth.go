// Package cursor implements native Go access to a Cursor subscription:
// PKCE OAuth (login → poll → refresh) and a token store. This is phase 1;
// the Agent (chat) protocol client is built on top of these credentials.
//
// Endpoints reverse-engineered from Cursor's CLI login flow:
//
//	login: https://cursor.com/loginDeepControl?challenge&uuid&mode=login&redirectTarget=cli
//	poll:  https://api2.cursor.sh/auth/poll?uuid=&verifier=
//	refresh: POST https://api2.cursor.sh/auth/exchange_user_api_key (Bearer <refresh>, body {})
package cursor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	loginURL   = "https://cursor.com/loginDeepControl"
	pollURL    = "https://api2.cursor.sh/auth/poll"
	refreshURL = "https://api2.cursor.sh/auth/exchange_user_api_key"

	pollMaxAttempts = 150
	pollBaseDelay   = 1 * time.Second
	pollMaxDelay    = 10 * time.Second
	pollBackoff     = 1.2
)

// AuthParams is one PKCE login attempt.
type AuthParams struct {
	Verifier  string
	Challenge string
	UUID      string
	LoginURL  string
}

// Credentials are the stored tokens.
type Credentials struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"` // unix ms; access valid until here
}

// Expired reports whether the access token is past (or near) its expiry.
func (c Credentials) Expired() bool { return nowMs() >= c.Expires }

// GenerateAuthParams builds a PKCE verifier/challenge + login URL.
func GenerateAuthParams() (AuthParams, error) {
	verifier, challenge, err := pkce()
	if err != nil {
		return AuthParams{}, err
	}
	uuid, err := randomUUID()
	if err != nil {
		return AuthParams{}, err
	}
	q := url.Values{
		"challenge":      {challenge},
		"uuid":           {uuid},
		"mode":           {"login"},
		"redirectTarget": {"cli"},
	}
	return AuthParams{
		Verifier:  verifier,
		Challenge: challenge,
		UUID:      uuid,
		LoginURL:  loginURL + "?" + q.Encode(),
	}, nil
}

// Poll waits for the user to complete browser login, returning tokens.
// progress (optional) is called each attempt.
func Poll(uuid, verifier string, progress func(attempt int)) (Credentials, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	delay := pollBaseDelay
	consecErr := 0

	for attempt := 0; attempt < pollMaxAttempts; attempt++ {
		time.Sleep(delay)
		if progress != nil {
			progress(attempt)
		}
		q := url.Values{"uuid": {uuid}, "verifier": {verifier}}
		resp, err := hc.Get(pollURL + "?" + q.Encode())
		if err != nil {
			consecErr++
			if consecErr >= 3 {
				return Credentials{}, fmt.Errorf("too many poll errors: %w", err)
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusNotFound:
			consecErr = 0
			delay = time.Duration(float64(delay) * pollBackoff)
			if delay > pollMaxDelay {
				delay = pollMaxDelay
			}
		case resp.StatusCode == http.StatusOK:
			var t struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.Unmarshal(body, &t); err != nil {
				return Credentials{}, fmt.Errorf("decode poll: %w", err)
			}
			if t.AccessToken == "" {
				consecErr = 0
				continue
			}
			return Credentials{
				Access:  t.AccessToken,
				Refresh: t.RefreshToken,
				Expires: tokenExpiry(t.AccessToken),
			}, nil
		default:
			consecErr++
			if consecErr >= 3 {
				return Credentials{}, fmt.Errorf("poll failed: %d %s", resp.StatusCode, string(body))
			}
		}
	}
	return Credentials{}, fmt.Errorf("cursor auth timed out")
}

// Refresh exchanges the refresh token for a fresh access token.
func Refresh(refresh string) (Credentials, error) {
	req, err := http.NewRequest(http.MethodPost, refreshURL, jsonBody("{}"))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Authorization", "Bearer "+refresh)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return Credentials{}, fmt.Errorf("refresh failed: %d %s", resp.StatusCode, string(body))
	}
	var t struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return Credentials{}, err
	}
	newRefresh := t.RefreshToken
	if newRefresh == "" {
		newRefresh = refresh
	}
	return Credentials{
		Access:  t.AccessToken,
		Refresh: newRefresh,
		Expires: tokenExpiry(t.AccessToken),
	}, nil
}

// pkce returns a base64url verifier (96 random bytes) + its SHA-256 challenge.
func pkce() (verifier, challenge string, err error) {
	b := make([]byte, 96)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// tokenExpiry reads a JWT's exp claim (minus a 5-minute safety margin),
// falling back to 1 hour out.
func tokenExpiry(token string) int64 {
	fallback := nowMs() + 3600*1000
	parts := splitDots(token)
	if len(parts) != 3 || parts[1] == "" {
		return fallback
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fallback
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return fallback
	}
	return claims.Exp*1000 - 5*60*1000
}
