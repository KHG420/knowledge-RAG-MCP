package knowledge

import (
	"strings"
	"testing"
)

func TestChunkText_EmptyInput(t *testing.T) {
	chunks := ChunkText("")
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkText_WhitespaceOnly(t *testing.T) {
	chunks := ChunkText("   \n\n  \t  ")
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkText_SingleParagraph(t *testing.T) {
	text := "This is a single paragraph of text that should be kept as one chunk."
	chunks := ChunkText(text)
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
	if !strings.Contains(chunks[0].Content, "single paragraph") {
		t.Errorf("chunk content mismatch: %q", chunks[0].Content)
	}
}

func TestChunkText_BasicParagraphs(t *testing.T) {
	text := "First long paragraph with enough text to avoid merging.\n\nSecond long paragraph also with enough text to stay separate."
	chunks := ChunkText(text)
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
	// Short paragraphs may be merged, so just check we have content
}

func TestChunkText_LongParagraph(t *testing.T) {
	// Build a long paragraph with sentence boundaries so splitLong works
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("This is a sentence. ")
	}
	text := sb.String()
	chunks := ChunkText(text)
	if len(chunks) < 2 {
		t.Errorf("expected long paragraph to be split, got %d chunks", len(chunks))
	}
}

func TestChunkText_WithSectionHeaders(t *testing.T) {
	text := "# Introduction\n\nThis is the introduction.\n\n# Methods\n\nThis is the methods section."
	chunks := ChunkText(text)
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
}

func TestChunkText_CJK(t *testing.T) {
	text := "第一段内容很长很长很长很长很长很长很长很长很长很长。\n\n第二段内容也很长很长很长很长很长很长很长很长很长很长。"
	chunks := ChunkText(text)
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
}

func TestChunkText_CRLFNewlines(t *testing.T) {
	text := "Paragraph one with enough text to stay as an independent chunk.\r\n\r\nParagraph two also has enough characters."
	chunks := ChunkText(text)
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
}

func TestChunkText_HasContent(t *testing.T) {
	text := "Paragraph A with enough content.\n\nParagraph B with enough content too."
	chunks := ChunkText(text)
	for i, c := range chunks {
		if c.Content == "" {
			t.Errorf("chunk %d has empty Content", i)
		}
	}
}

func TestChunkTextContent_Nominal(t *testing.T) {
	text := "Hello world. This is content."
	chunks := ChunkTextContent(text)
	if len(chunks) == 0 {
		t.Error("expected at least 1 chunk")
	}
}

func TestClassifySectionRole(t *testing.T) {
	tests := []struct {
		heading string
		want    string
	}{
		{"Abstract", "abstract"},
		{"Introduction", "introduction"},
		{"Background", "related_work"},
		{"Methods", "methodology"},
		{"Results", "experiments"},
		{"Discussion", "conclusion"},
		{"Conclusion", "conclusion"},
		{"References", "references"},
		{"Related Work", "related_work"},
		{"Unknown Section Title", ""},
	}
	for _, tt := range tests {
		got := classifySectionRole(tt.heading)
		if got != tt.want {
			t.Errorf("classifySectionRole(%q) = %q, want %q", tt.heading, got, tt.want)
		}
	}
}
