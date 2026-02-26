package github

import "time"

type PullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number         int        `json:"number"`
		Title          string     `json:"title"`
		Body           string     `json:"body"`
		Merged         bool       `json:"merged"`
		MergedAt       *time.Time `json:"merged_at"`
		MergeCommitSHA string     `json:"merge_commit_sha"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type MergedPR struct {
	RepoFullName   string
	Number         int
	Title          string
	Body           string
	MergeCommitSHA string
	AuthorLogin    string
	MergedAt       time.Time
}

func ExtractMergedPR(e PullRequestEvent) (MergedPR, bool) {
	if e.Action != "closed" {
		return MergedPR{}, false
	}
	if !e.PullRequest.Merged {
		return MergedPR{}, false
	}
	if e.PullRequest.MergedAt == nil {
		return MergedPR{}, false
	}

	return MergedPR{
		RepoFullName:   e.Repository.FullName,
		Number:         e.PullRequest.Number,
		Title:          e.PullRequest.Title,
		Body:           e.PullRequest.Body,
		MergeCommitSHA: e.PullRequest.MergeCommitSHA,
		AuthorLogin:    e.PullRequest.User.Login,
		MergedAt:       *e.PullRequest.MergedAt,
	}, true
}
