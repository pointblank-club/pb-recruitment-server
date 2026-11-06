package judge0

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompilationGroupPayloadCompatibility(t *testing.T) {
	plain, err := json.Marshal(SubmissionRequest{LanguageID: 54})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "compilation_group") {
		t.Fatal("legacy request must omit group")
	}
	original := SubmissionRequest{LanguageID: 54, CompilationGroup: "a2e19397-8d16-43fb-b5ce-481ee63fc4f4"}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var recovered SubmissionRequest
	if err := json.Unmarshal(payload, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.CompilationGroup != original.CompilationGroup {
		t.Fatal("recovery snapshot lost compilation group")
	}
}
