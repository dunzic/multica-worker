package daemon

import (
	"strings"
	"testing"
)

func TestGroupTopicPromptPreservesAudienceAndPinnedAttachments(t *testing.T) {
	task := Task{TopicID: "topic", ChatType: "group", ChatMessage: "Current pinned request", ChatMessageAttachments: []ChatAttachmentMeta{{ID: "resume-1", Filename: "Candidate.docx"}}}
	prompt := BuildPrompt(task, "claude")
	for _, want := range []string{"Audience: group room", "pinned to one group topic message", "Current pinned request", "resume-1", "Candidate.docx", "attachment download"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	notice := sessionContinuityNoticeFor(task)
	if !strings.Contains(notice, "bounded topic history") || strings.Contains(notice, "multica issue comment") {
		t.Fatalf("wrong continuity advice: %s", notice)
	}
}
