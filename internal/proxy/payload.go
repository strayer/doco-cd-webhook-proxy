package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
)

type Repository struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

type Pusher struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type GitHubPushEvent struct {
	Ref        string     `json:"ref"`
	Before     string     `json:"before"`
	After      string     `json:"after"`
	Repository Repository `json:"repository"`
	Pusher     Pusher     `json:"pusher"`
}

func ParsePayload(body []byte) (GitHubPushEvent, error) {
	var event GitHubPushEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return GitHubPushEvent{}, fmt.Errorf("invalid JSON payload: %w", err)
	}

	if err := validatePayload(&event); err != nil {
		return GitHubPushEvent{}, err
	}

	return event, nil
}

func (e GitHubPushEvent) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

func validatePayload(event *GitHubPushEvent) error {
	checks := []struct {
		value string
		field string
	}{
		{event.Ref, "ref"},
		{event.Before, "before"},
		{event.After, "after"},
		{event.Repository.Name, "repository.name"},
		{event.Repository.FullName, "repository.full_name"},
		{event.Repository.CloneURL, "repository.clone_url"},
		{event.Pusher.Name, "pusher.name"},
		{event.Pusher.Email, "pusher.email"},
	}

	for _, c := range checks {
		if c.value == "" {
			return errors.New("missing required field: " + c.field)
		}
	}

	return nil
}
