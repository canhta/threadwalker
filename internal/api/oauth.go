package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
)

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) string {
	link, ok := s.take(s.links, r.URL.Query().Get("k"))
	if !ok {
		http.Error(w, "This link is invalid or has expired.", http.StatusForbidden)
		return "link"
	}
	state, err := s.token()
	if err != nil {
		http.Error(w, "Unavailable.", http.StatusInternalServerError)
		return "random"
	}
	s.mu.Lock()
	sweep(s.states, s.o.Now())
	if len(s.states) >= maxPending {
		s.mu.Unlock()
		http.Error(w, "Try again later.", http.StatusServiceUnavailable)
		return "state_full"
	}
	s.states[state] = pending{link.agentID, s.o.Now().Add(stateTTL)}
	s.mu.Unlock()
	q := url.Values{"client_id": {s.o.AppID}, "redirect_uri": {s.RedirectURI()}, "scope": {strings.Join(Scopes, ",")}, "response_type": {"code"}, "state": {state}}
	http.Redirect(w, r, s.o.AuthorizeURL+"?"+q.Encode(), http.StatusFound)
	return "redirected"
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) string {
	q := r.URL.Query()
	st, ok := s.take(s.states, q.Get("state"))
	if !ok {
		http.Error(w, "This sign-in is invalid or has expired.", http.StatusForbidden)
		return "state"
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "Sign-in was not granted.", http.StatusBadRequest)
		return "denied"
	}
	agent := s.byAgent[st.agentID]
	token, expiresIn, userID, err := s.exchange(r.Context(), code)
	if err != nil || userID != agent.UserID {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		if err == nil {
			return "wrong_account"
		}
		return "exchange"
	}
	if err = s.o.Tokens.Install(r.Context(), agent.ID, token, expiresIn); err != nil {
		http.Error(w, "Sign-in failed.", http.StatusInternalServerError)
		return "store"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Threads is connected. You can close this page.\n")
	return "connected"
}

// exchange trades the code for a short-lived token, then that for a long-lived one.
func (s *Server) exchange(ctx context.Context, code string) (token config.Secret, expiresIn time.Duration, userID string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var short struct {
		AccessToken string      `json:"access_token"`
		UserID      json.Number `json:"user_id"`
	}
	form := url.Values{"client_id": {s.o.AppID}, "client_secret": {s.o.AppSecret.Expose()}, "grant_type": {"authorization_code"}, "redirect_uri": {s.RedirectURI()}, "code": {strings.TrimSuffix(code, "#_")}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.o.GraphURL+"/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return token, 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err = s.call(req, &short); err != nil || short.AccessToken == "" {
		return token, 0, "", errors.New("exchanging authorization code failed")
	}
	long := url.Values{"grant_type": {"th_exchange_token"}, "client_secret": {s.o.AppSecret.Expose()}, "access_token": {short.AccessToken}}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, s.o.GraphURL+"/access_token?"+long.Encode(), nil)
	if err != nil {
		return token, 0, "", err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err = s.call(req, &out); err != nil || out.AccessToken == "" || out.ExpiresIn <= 0 {
		return token, 0, "", errors.New("exchanging for a long-lived token failed")
	}
	return config.NewSecret(out.AccessToken), time.Duration(out.ExpiresIn) * time.Second, short.UserID.String(), nil
}

func (s *Server) call(req *http.Request, out any) error {
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return errors.New("token request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token request status %s", strconv.Itoa(resp.StatusCode))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out)
}
