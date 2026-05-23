package proxy

import (
	"encoding/json"
	"testing"
)

func TestParsePayload(t *testing.T) {
	validPayload := `{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"repository": {
			"name": "my-repo",
			"full_name": "org/my-repo",
			"clone_url": "https://github.com/org/my-repo.git"
		},
		"pusher": {
			"name": "octocat",
			"email": "octocat@github.com"
		}
	}`

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "valid payload",
			body:    validPayload,
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			body:    `{not json`,
			wantErr: true,
		},
		{
			name:    "empty body",
			body:    ``,
			wantErr: true,
		},
		{
			name: "missing ref",
			body: `{
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "empty ref",
			body: `{
				"ref": "",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing before",
			body: `{
				"ref": "refs/heads/main",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing after",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing repository",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing repository name",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing repository full_name",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing repository clone_url",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r"},
				"pusher": {"name": "u", "email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing pusher",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"}
			}`,
			wantErr: true,
		},
		{
			name: "missing pusher name",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"email": "u@x.com"}
			}`,
			wantErr: true,
		},
		{
			name: "missing pusher email",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git"},
				"pusher": {"name": "u"}
			}`,
			wantErr: true,
		},
		{
			name: "extra fields are ignored",
			body: `{
				"ref": "refs/heads/main",
				"before": "0000000000000000000000000000000000000000",
				"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"repository": {"name": "r", "full_name": "o/r", "clone_url": "https://github.com/o/r.git", "id": 12345},
				"pusher": {"name": "u", "email": "u@x.com"},
				"sender": {"login": "octocat"},
				"head_commit": {"id": "abc"}
			}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParsePayload([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if event.Ref == "" {
				t.Error("expected non-empty Ref")
			}
		})
	}
}

func TestParsePayloadFields(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"repository": {
			"name": "my-repo",
			"full_name": "org/my-repo",
			"clone_url": "https://github.com/org/my-repo.git"
		},
		"pusher": {
			"name": "octocat",
			"email": "octocat@github.com"
		}
	}`)

	event, err := ParsePayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q, want %q", event.Ref, "refs/heads/main")
	}
	if event.Before != "0000000000000000000000000000000000000000" {
		t.Errorf("Before = %q, want %q", event.Before, "0000000000000000000000000000000000000000")
	}
	if event.After != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("After = %q, want %q", event.After, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	if event.Repository.Name != "my-repo" {
		t.Errorf("Repository.Name = %q, want %q", event.Repository.Name, "my-repo")
	}
	if event.Repository.FullName != "org/my-repo" {
		t.Errorf("Repository.FullName = %q, want %q", event.Repository.FullName, "org/my-repo")
	}
	if event.Repository.CloneURL != "https://github.com/org/my-repo.git" {
		t.Errorf("Repository.CloneURL = %q, want %q", event.Repository.CloneURL, "https://github.com/org/my-repo.git")
	}
	if event.Pusher.Name != "octocat" {
		t.Errorf("Pusher.Name = %q, want %q", event.Pusher.Name, "octocat")
	}
	if event.Pusher.Email != "octocat@github.com" {
		t.Errorf("Pusher.Email = %q, want %q", event.Pusher.Email, "octocat@github.com")
	}
}

func TestGitHubPushEventMarshal(t *testing.T) {
	event := GitHubPushEvent{
		Ref:    "refs/heads/main",
		Before: "0000000000000000000000000000000000000000",
		After:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Repository: Repository{
			Name:     "my-repo",
			FullName: "org/my-repo",
			CloneURL: "https://github.com/org/my-repo.git",
		},
		Pusher: Pusher{
			Name:  "octocat",
			Email: "octocat@github.com",
		},
	}

	data, err := event.Marshal()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("marshaled data is not valid JSON: %v", err)
	}

	if parsed["ref"] != "refs/heads/main" {
		t.Errorf("ref = %v, want %q", parsed["ref"], "refs/heads/main")
	}
	if parsed["before"] != "0000000000000000000000000000000000000000" {
		t.Errorf("before = %v, want %q", parsed["before"], "0000000000000000000000000000000000000000")
	}
	if parsed["after"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("after = %v, want %q", parsed["after"], "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}

	repo, ok := parsed["repository"].(map[string]interface{})
	if !ok {
		t.Fatal("repository is not an object")
	}
	if repo["name"] != "my-repo" {
		t.Errorf("repository.name = %v, want %q", repo["name"], "my-repo")
	}
	if repo["full_name"] != "org/my-repo" {
		t.Errorf("repository.full_name = %v, want %q", repo["full_name"], "org/my-repo")
	}
	if repo["clone_url"] != "https://github.com/org/my-repo.git" {
		t.Errorf("repository.clone_url = %v, want %q", repo["clone_url"], "https://github.com/org/my-repo.git")
	}

	pusher, ok := parsed["pusher"].(map[string]interface{})
	if !ok {
		t.Fatal("pusher is not an object")
	}
	if pusher["name"] != "octocat" {
		t.Errorf("pusher.name = %v, want %q", pusher["name"], "octocat")
	}
	if pusher["email"] != "octocat@github.com" {
		t.Errorf("pusher.email = %v, want %q", pusher["email"], "octocat@github.com")
	}
}

func TestGitHubPushEventMarshalExcludesExtraFields(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main",
		"before": "0000000000000000000000000000000000000000",
		"after": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"repository": {
			"name": "r",
			"full_name": "o/r",
			"clone_url": "https://github.com/o/r.git",
			"id": 12345,
			"private": true
		},
		"pusher": {"name": "u", "email": "u@x.com"},
		"sender": {"login": "octocat"},
		"head_commit": {"id": "abc123"}
	}`)

	event, err := ParsePayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := event.Marshal()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("marshaled data is not valid JSON: %v", err)
	}

	allowedTopLevel := map[string]bool{"ref": true, "before": true, "after": true, "repository": true, "pusher": true}
	for key := range parsed {
		if !allowedTopLevel[key] {
			t.Errorf("unexpected top-level key in marshaled output: %q", key)
		}
	}

	repo, ok := parsed["repository"].(map[string]interface{})
	if !ok {
		t.Fatal("repository is not an object")
	}
	allowedRepo := map[string]bool{"name": true, "full_name": true, "clone_url": true}
	for key := range repo {
		if !allowedRepo[key] {
			t.Errorf("unexpected repository key in marshaled output: %q", key)
		}
	}

	pusher, ok := parsed["pusher"].(map[string]interface{})
	if !ok {
		t.Fatal("pusher is not an object")
	}
	allowedPusher := map[string]bool{"name": true, "email": true}
	for key := range pusher {
		if !allowedPusher[key] {
			t.Errorf("unexpected pusher key in marshaled output: %q", key)
		}
	}
}
