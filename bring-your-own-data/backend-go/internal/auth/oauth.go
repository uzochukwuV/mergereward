package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	githubAuthURL  = "https://github.com/login/oauth/authorize"
	githubTokenURL = "https://github.com/login/oauth/access_token"
	githubUserURL  = "https://api.github.com/user"
)

// GitHubUser is the subset of the GitHub user API response we need.
type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Enabled returns true when GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET are set.
func Enabled() bool {
	return os.Getenv("GITHUB_CLIENT_ID") != "" && os.Getenv("GITHUB_CLIENT_SECRET") != ""
}

// AuthURL builds the GitHub OAuth authorization URL for the given state nonce.
func AuthURL(state string) string {
	params := url.Values{
		"client_id": {os.Getenv("GITHUB_CLIENT_ID")},
		"scope":     {"read:user"},
		"state":     {state},
	}
	return githubAuthURL + "?" + params.Encode()
}

// ExchangeCode exchanges a GitHub OAuth code for an access token.
func ExchangeCode(code string) (string, error) {
	body := url.Values{
		"client_id":     {os.Getenv("GITHUB_CLIENT_ID")},
		"client_secret": {os.Getenv("GITHUB_CLIENT_SECRET")},
		"code":          {code},
	}

	req, err := http.NewRequest(http.MethodPost, githubTokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Err         string `json:"error"`
		ErrDesc     string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Err != "" {
		return "", fmt.Errorf("github oauth: %s: %s", result.Err, result.ErrDesc)
	}
	return result.AccessToken, nil
}

// FetchUser calls the GitHub /user API with the given access token.
func FetchUser(accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequest(http.MethodGet, githubUserURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github user api: status %d", resp.StatusCode)
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}
