package knowledge

import (
	"strings"
	"testing"
)

// TestDefaultToolDescriptionsAreGeneric verifies the built-in descriptions are
// domain-neutral, do not promise semantic models, and point follow-up reads at
// the provenance fields returned by search.
func TestDefaultToolDescriptionsAreGeneric(t *testing.T) {
	if n := len(DefaultSearchDesc); n > 2200 {
		t.Errorf("DefaultSearchDesc is %d chars; keep the default concise", n)
	}
	for _, banned := range []string{"maritime", "Ikeda", "耐波性", "船舶", "ship"} {
		if strings.Contains(DefaultSearchDesc, banned) {
			t.Errorf("DefaultSearchDesc still contains domain-specific term %q", banned)
		}
	}
	if !strings.Contains(DefaultSearchDesc, "coverage") {
		t.Error("DefaultSearchDesc should explain the coverage field")
	}
	if strings.Contains(strings.ToLower(DefaultSearchDesc), "always available") {
		t.Error("DefaultSearchDesc must not promise models are always available")
	}

	for _, want := range []string{"kb_name", "document.id", "location.chunk_id"} {
		if !strings.Contains(DefaultReadDesc, want) {
			t.Errorf("DefaultReadDesc should route follow-up reads using %q", want)
		}
	}
	if !strings.Contains(DefaultReadDesc, "unknown") {
		t.Error("DefaultReadDesc should explain that relevance/completeness are unknown")
	}
	if len(DefaultReadKbNameDesc) == 0 || len(DefaultListKBsDesc) == 0 {
		t.Error("KB/list descriptions must not be empty")
	}
}
