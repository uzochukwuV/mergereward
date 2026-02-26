package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

type ScoreRequest struct {
	RepoFullName string
	PRNumber     int
	Title        string
	Body         string
	MergeSHA     string
}

func Enabled() bool {
	return os.Getenv("OPENAI_API_KEY") != ""
}

type chatReq struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

type aiAnswer struct {
	Score   int    `json:"score"`
	Summary string `json:"summary"`
}

func ScoreMergedPR(req ScoreRequest) (score int, summary string, err error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return 0, "", errors.New("OPENAI_API_KEY is not set")
	}

	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	system := "You are a code review assistant scoring merged pull requests for bounty payouts. Output JSON with fields: score (0-100), summary (1-3 sentences)."
	user := fmt.Sprintf("Repo: %s\nPR: #%d\nTitle: %s\nBody: %s\nMerge commit: %s\n\nScore the PR quality and relevance to a linked issue. If the description looks like spam/irrelevant, give a low score.", req.RepoFullName, req.PRNumber, req.Title, req.Body, req.MergeSHA)

	payload, _ := json.Marshal(chatReq{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})

	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("openai api status %d", resp.StatusCode)
	}

	var out chatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, "", err
	}
	if len(out.Choices) == 0 {
		return 0, "", errors.New("openai response missing choices")
	}

	var ans aiAnswer
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &ans); err != nil {
		// best-effort fallback: treat whole content as summary
		return 0, out.Choices[0].Message.Content, nil
	}

	if ans.Score < 0 {
		ans.Score = 0
	}
	if ans.Score > 100 {
		ans.Score = 100
	}
	return ans.Score, ans.Summary, nil
}
