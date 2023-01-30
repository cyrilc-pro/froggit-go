package webhookparser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jfrog/froggit-go/vcsutils"
)

// BitbucketCloudWebhook represents an incoming webhook on Bitbucket cloud
type BitbucketCloudWebhook struct {
	request *http.Request
}

// NewBitbucketCloudWebhookWebhook create a new BitbucketCloudWebhook instance
func NewBitbucketCloudWebhookWebhook(request *http.Request) *BitbucketCloudWebhook {
	return &BitbucketCloudWebhook{
		request: request,
	}
}

func (webhook *BitbucketCloudWebhook) Parse(token []byte) (*WebhookInfo, error) {
	return validateAndParseHttpRequest(webhook, token, webhook.request)
}

func (webhook *BitbucketCloudWebhook) validatePayload(token []byte) ([]byte, error) {
	keys, tokenParamsExist := webhook.request.URL.Query()["token"]
	if len(token) > 0 || tokenParamsExist {
		if keys[0] != string(token) {
			return nil, errors.New("token mismatch")
		}
	}
	payload := new(bytes.Buffer)
	if _, err := payload.ReadFrom(webhook.request.Body); err != nil {
		return nil, err
	}
	return payload.Bytes(), nil
}

func (webhook *BitbucketCloudWebhook) parseIncomingWebhook(payload []byte) (*WebhookInfo, error) {
	bitbucketCloudWebHook := &bitbucketCloudWebHook{}
	err := json.Unmarshal(payload, bitbucketCloudWebHook)
	if err != nil {
		return nil, err
	}

	event := webhook.request.Header.Get(EventHeaderKey)
	switch event {
	case "repo:push":
		return webhook.parsePushEvent(bitbucketCloudWebHook), nil
	case "pullrequest:created":
		return webhook.parsePrEvents(bitbucketCloudWebHook, vcsutils.PrOpened), nil
	case "pullrequest:updated":
		return webhook.parsePrEvents(bitbucketCloudWebHook, vcsutils.PrEdited), nil
	case "pullrequest:fulfilled":
		return webhook.parsePrEvents(bitbucketCloudWebHook, vcsutils.PrMerged), nil
	case "pullrequest:rejected":
		return webhook.parsePrEvents(bitbucketCloudWebHook, vcsutils.PrRejected), nil
	}
	return nil, nil
}

func (webhook *BitbucketCloudWebhook) parsePushEvent(bitbucketCloudWebHook *bitbucketCloudWebHook) *WebhookInfo {
	firstChange := bitbucketCloudWebHook.Push.Changes[0]
	lastCommit := firstChange.New.Target
	beforeCommitHash := webhook.parentOfLastCommit(lastCommit)
	return &WebhookInfo{
		TargetRepositoryDetails: webhook.parseRepoFullName(bitbucketCloudWebHook.Repository.FullName),
		TargetBranch:            webhook.getBranchName(firstChange),
		PullRequestId:           0,                        // unused for push event
		SourceRepositoryDetails: WebHookInfoRepoDetails{}, // unused for push event
		SourceBranch:            "",                       // unused for push event
		Timestamp:               lastCommit.Date.UTC().Unix(),
		Event:                   vcsutils.Push,
		Commit: WebHookInfoCommit{
			Hash:    lastCommit.Hash,
			Message: lastCommit.Message,
			Url:     lastCommit.Links.Html.Ref,
		},
		BeforeCommit: WebHookInfoCommit{
			Hash:    beforeCommitHash,
			Message: "",
			Url:     "",
		},
		BranchStatus: webhook.branchStatus(firstChange),
		TriggeredBy: WebHookInfoUser{
			Login:       bitbucketCloudWebHook.Actor.Nickname,
			DisplayName: "",
			Email:       "",
			AvatarUrl:   "",
		},
		Committer: WebHookInfoUser{
			Login:       webhook.login(bitbucketCloudWebHook, lastCommit),
			DisplayName: "",
			Email:       "",
			AvatarUrl:   "",
		},
		Author: WebHookInfoUser{
			Login:       webhook.login(bitbucketCloudWebHook, lastCommit),
			DisplayName: "",
			Email:       webhook.email(lastCommit),
			AvatarUrl:   "",
		},
		CompareUrl: webhook.compareURL(bitbucketCloudWebHook, lastCommit, beforeCommitHash),
	}
}

func (webhook *BitbucketCloudWebhook) compareURL(bitbucketCloudWebHook *bitbucketCloudWebHook,
	lastCommit bitbucketCommit, beforeCommitHash string) string {
	if lastCommit.Hash == "" || beforeCommitHash == "" {
		return ""
	}
	return fmt.Sprintf("https://bitbucket.org/%s/branches/compare/%s..%s#diff",
		bitbucketCloudWebHook.Repository.FullName, lastCommit.Hash, beforeCommitHash)
}

func (webhook *BitbucketCloudWebhook) getBranchName(firstChange bitbucketChange) string {
	branchName := firstChange.New.Name
	if branchName == "" {
		branchName = firstChange.Old.Name
	}
	return branchName
}

func (webhook *BitbucketCloudWebhook) email(lastCommit bitbucketCommit) string {
	email := lastCommit.Author.Raw
	parsedEmail, err := mail.ParseAddress(lastCommit.Author.Raw)
	if err == nil && parsedEmail != nil {
		email = parsedEmail.Address
	}
	return email
}

func (webhook *BitbucketCloudWebhook) parsePrEvents(bitbucketCloudWebHook *bitbucketCloudWebHook, event vcsutils.WebhookEvent) *WebhookInfo {
	return &WebhookInfo{
		PullRequestId:           bitbucketCloudWebHook.PullRequest.ID,
		TargetRepositoryDetails: webhook.parseRepoFullName(bitbucketCloudWebHook.PullRequest.Destination.Repository.FullName),
		TargetBranch:            bitbucketCloudWebHook.PullRequest.Destination.Branch.Name,
		SourceRepositoryDetails: webhook.parseRepoFullName(bitbucketCloudWebHook.PullRequest.Source.Repository.FullName),
		SourceBranch:            bitbucketCloudWebHook.PullRequest.Source.Branch.Name,
		Timestamp:               bitbucketCloudWebHook.PullRequest.UpdatedOn.UTC().Unix(),
		Event:                   event,
	}
}

func (webhook *BitbucketCloudWebhook) parseRepoFullName(fullName string) WebHookInfoRepoDetails {
	// From https://support.atlassian.com/bitbucket-cloud/docs/event-payloads/#Repository
	// "full_name : The workspace and repository slugs joined with a '/'."
	split := strings.Split(fullName, "/")
	return WebHookInfoRepoDetails{
		Name:  split[1],
		Owner: split[0],
	}
}

func (webhook *BitbucketCloudWebhook) parentOfLastCommit(lastCommit bitbucketCommit) string {
	if len(lastCommit.Parents) == 0 {
		return ""
	}
	return lastCommit.Parents[0].Hash
}

func (webhook *BitbucketCloudWebhook) login(hook *bitbucketCloudWebHook, lastCommit bitbucketCommit) string {
	if lastCommit.Author.User.Nickname != "" {
		return lastCommit.Author.User.Nickname
	}
	return hook.Actor.Nickname
}

func (webhook *BitbucketCloudWebhook) branchStatus(change bitbucketChange) WebHookInfoBranchStatus {
	existsAfter := change.New.Name != ""
	existedBefore := change.Old.Name != ""
	return branchStatus(existedBefore, existsAfter)
}

type bitbucketCloudWebHook struct {
	Push        bitbucketPush            `json:"push,omitempty"`
	PullRequest bitbucketPullRequest     `json:"pullrequest,omitempty"`
	Repository  bitbucketCloudRepository `json:"repository,omitempty"`
	Actor       struct {
		Nickname string `json:"nickname,omitempty"`
	} `json:"actor,omitempty"`
}

type bitbucketPullRequest struct {
	ID          int                        `json:"id,omitempty"`
	Source      bitbucketCloudPrRepository `json:"source,omitempty"`
	Destination bitbucketCloudPrRepository `json:"destination,omitempty"`
	UpdatedOn   time.Time                  `json:"updated_on,omitempty"`
}

type bitbucketPush struct {
	Changes []bitbucketChange `json:"changes,omitempty"`
}
type bitbucketChange struct {
	New struct {
		Name   string          `json:"name,omitempty"` // Branch name
		Target bitbucketCommit `json:"target,omitempty"`
	} `json:"new,omitempty"`
	Old struct {
		Name string `json:"name,omitempty"` // Branch name
	} `json:"old,omitempty"`
}

type bitbucketCommit struct {
	Date    time.Time `json:"date,omitempty"`    // Timestamp
	Hash    string    `json:"hash,omitempty"`    // Commit Hash
	Message string    `json:"message,omitempty"` // Commit message
	Author  struct {
		Raw  string `json:"raw,omitempty"` // Commit author
		User struct {
			Nickname string `json:"nickname,omitempty"`
		} `json:"user,omitempty"`
	} `json:"author,omitempty"`
	Links struct {
		Html struct {
			Ref string `json:"ref,omitempty"` // Commit URL
		} `json:"html,omitempty"`
	} `json:"links,omitempty"`
	Parents []struct {
		Hash string `json:"hash,omitempty"` // Commit Hash
	} `json:"parents,omitempty"`
}

type bitbucketCloudRepository struct {
	FullName string `json:"full_name,omitempty"` // Repository full name
}

type bitbucketCloudPrRepository struct {
	Repository bitbucketCloudRepository `json:"repository,omitempty"`
	Branch     struct {
		Name string `json:"name,omitempty"` // Branch name
	} `json:"branch,omitempty"`
}
