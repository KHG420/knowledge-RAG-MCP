package knowledge

import (
	"context"
	"reflect"
	"testing"
)

func TestContainsCJK(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"hello world", false},
		{"123abc", false},
		{"横摇", true},
		{"CFD 计算", true},
		{"Ikeda方法", true},
		{"", false},
		{"日本語", true},  // Hiragana
		{"한국어", true},  // Hangul
		{"ship motion", false},
	}
	for _, tt := range tests {
		got := containsCJK(tt.input)
		if got != tt.want {
			t.Errorf("containsCJK(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestDefaultConstraintsUseDeployedKnowledgeBaseName(t *testing.T) {
	constraints := defaultConstraints()
	if len(constraints["横摇论文"]) == 0 {
		t.Fatal("roll-domain constraints are not bound to the deployed KB name")
	}
	if _, stale := constraints["ship_motion"]; stale {
		t.Fatal("constraints still target a non-existent KB name")
	}
}

func TestRouteSelectsOnlyTheExplicitlyMatchedDomain(t *testing.T) {
	router := NewKBRouter(nil)
	router.SetKBDescs([]KBDesc{
		{Name: "横摇论文", Desc: "船舶横摇动力学与稳性专业论文"},
		{Name: "航海知识库", Desc: "航海通用与海事专业知识"},
	})

	roll := router.Route(context.Background(), "横摇阻尼和遭遇频率如何影响参数横摇？", nil)
	if !reflect.DeepEqual(roll.Selected, []string{"横摇论文"}) {
		t.Fatalf("unexpected roll route: %v", roll.Selected)
	}
	legal := router.Route(context.Background(), "London arbitration 航速油耗索赔", nil)
	if !reflect.DeepEqual(legal.Selected, []string{"航海知识库"}) {
		t.Fatalf("unexpected maritime-law route: %v", legal.Selected)
	}
}

func TestTokenizeForRoute(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "pure CJK single term",
			input: "横摇阻尼计算",
			// Per-char unigram then bigram: 横,摇,横摇,阻,摇阻,尼,阻尼,计,尼计,算,计算
			want: []string{"横", "摇", "横摇", "阻", "摇阻", "尼", "阻尼", "计", "尼计", "算", "计算"},
		},
		{
			name:  "pure latin",
			input: "ship motion CFD",
			want:  []string{"ship", "motion", "cfd"},
		},
		{
			name:  "mixed CJK and latin (space separated)",
			input: "CFD 横摇",
			// "cfd" then "横","摇","横摇"
			want: []string{"cfd", "横", "摇", "横摇"},
		},
		{
			name:  "punctuation stripped",
			input: "hello, world!",
			want:  []string{"hello", "world"},
		},
		{
			name:  "empty string",
			input: "",
			want:  []string{},
		},
		{
			name:  "only whitespace and punctuation",
			input: "  , . ! ",
			want:  []string{},
		},
		{
			name:  "duplicate tokens deduplicated",
			input: "CFD CFD 横摇 横摇",
			// CFD appears once, 横→摇→横摇 once
			want: []string{"cfd", "横", "摇", "横摇"},
		},
		{
			name:  "mixed with embedded CJK and latin",
			input: "Ikeda方法 船舶CFD",
			// "Ikeda方法": i,k,ik,e,ke,d,ed,a,da,方,a方,法,方法
			// "船舶CFD": 船,舶,船舶,c,舶c,f,cf,d,fd — but "d" already seen from "Ikeda方法"
			want: []string{"i", "k", "ik", "e", "ke", "d", "ed", "a", "da", "方", "a方", "法", "方法", "船", "舶", "船舶", "c", "舶c", "f", "cf", "fd"},
		},
		{
			name:  "CJK with latin word",
			input: "船舶 seakeeping 耐波性",
			want:  []string{"船", "舶", "船舶", "seakeeping", "耐", "波", "耐波", "性", "波性"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenizeForRoute(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("tokenizeForRoute(%q) =\n  got:  %v\n  want: %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTokenizeForRoute_CJKRepeatChar(t *testing.T) {
	// 瑟瑟 — two identical CJK chars
	got := tokenizeForRoute("瑟瑟")
	// unigrams: 瑟 (once — dedup), bigram: 瑟瑟
	want := []string{"瑟", "瑟瑟"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTokenizeForRoute_LongCJK(t *testing.T) {
	// A longer CJK phrase — ensure no panic, reasonable output
	got := tokenizeForRoute("参数横摇同步横摇非线性")
	if len(got) == 0 {
		t.Error("expected non-empty tokens for long CJK input")
	}
	// Check key bigrams are present.
	has := func(s string) bool {
		for _, g := range got {
			if g == s {
				return true
			}
		}
		return false
	}
	if !has("参数") || !has("横摇") || !has("同步") {
		t.Errorf("missing expected bigrams, got: %v", got)
	}
}
