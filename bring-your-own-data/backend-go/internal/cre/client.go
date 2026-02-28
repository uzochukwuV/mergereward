package cre

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"
)

type Client struct {
	TriggerURL string
	Token      string
}

type MergeEvent struct {
	BountyID     string `json:"bountyId"`
	RepoID       string `json:"repoId"`
	IssueNumber  int    `json:"issueNumber"`
	PRNumber     int    `json:"prNumber"`
	DeveloperGitHub string `json:"developerGitHub"`
}

func NewFromEnv() (*Client, error) {
	url := os.Getenv("CRE_TRIGGER_URL")
	if url == "" {
		return nil, errors.New("CRE_TRIGGER_URL is required")
	}
	tok := os.Getenv("CRE_TRIGGER_TOKEN")
	if tok == "" {
		return nil, errors.New("CRE_TRIGGER_TOKEN is required")
	}
	return &Client{TriggerURL: url, Token: tok}, nil
}

func (c *Client) PostMergeEvent(evt MergeEvent) (*http.Response, error) {
	b, err := json.Marshal(evt)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.TriggerURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)

	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}
