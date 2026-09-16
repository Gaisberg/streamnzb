package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jhinrules "github.com/dreulavelle/jhin/rules"
)

func getCapabilities(t *testing.T, s *Server, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/capabilities", nil)
	if admin {
		req = asAdmin(req)
	}
	rec := httptest.NewRecorder()
	s.handleCapabilities(rec, req)
	return rec
}

// The report has to be usable as a client's only source: every part a client
// would branch on must be present and non-empty, since an omitted list reads
// as "not supported" rather than "not reported".
func TestHandleCapabilities(t *testing.T) {
	s := &Server{config: adminConfig()}
	rec := getCapabilities(t, s, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	// No stremio server is wired in this test, which is exactly the "dev"
	// case — the field must still be populated rather than empty.
	if got.StreamNZB == "" {
		t.Error("streamnzb version missing")
	}
	if got.Jhin.Version == "" {
		t.Error("jhin version missing")
	}
	if got.Jhin.SyntaxVersion != jhinrules.SyntaxVersion {
		t.Errorf("syntax_version = %d, want %d", got.Jhin.SyntaxVersion, jhinrules.SyntaxVersion)
	}

	if len(got.Rules.Fields) == 0 || len(got.Rules.Tiers) == 0 ||
		len(got.Rules.Functions) == 0 || len(got.Rules.Actions) == 0 || len(got.Rules.Scopes) == 0 {
		t.Fatalf("rule vocabulary incomplete: %d fields, %d tiers, %d functions, %d actions, %d scopes",
			len(got.Rules.Fields), len(got.Rules.Tiers), len(got.Rules.Functions),
			len(got.Rules.Actions), len(got.Rules.Scopes))
	}
	if len(got.Formatter.Fields) == 0 || len(got.Formatter.Helpers) == 0 || got.Formatter.Syntax == "" {
		t.Fatalf("formatter surface incomplete: %d fields, %d helpers, syntax %q",
			len(got.Formatter.Fields), len(got.Formatter.Helpers), got.Formatter.Syntax)
	}

	// The two questions the Discord report was actually about: is this
	// build's subtitle-language identity readable from a rule, and is it
	// readable from a template.
	if !hasRuleField(got, "probed.subtitleLanguages") {
		t.Error("probed.subtitleLanguages not reported as a rule field")
	}
	if !hasFormatField(got, ".Subtitles") {
		t.Error(".Subtitles not reported as a template field")
	}
	// A nested record has to arrive flattened to the path a template writes,
	// not as an opaque "record".
	if !hasFormatField(got, ".Probed.SubtitleLanguages") {
		t.Error("nested probe fields should be reported as dotted paths")
	}
	// A list of records contributes its element's fields too, or a template
	// author cannot tell what {{range .MatchedRules}} yields.
	if !hasFormatField(got, ".MatchedRules[].Name") {
		t.Error("list-of-record element fields should be reported under []")
	}
}

// The field list names which fact sources this install has, so it is not for
// every device that can reach the API.
func TestHandleCapabilitiesRequiresAdmin(t *testing.T) {
	s := &Server{config: adminConfig()}
	if rec := getCapabilities(t, s, false); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandleCapabilitiesRejectsPost(t *testing.T) {
	s := &Server{config: adminConfig()}
	req := asAdmin(httptest.NewRequest(http.MethodPost, "/api/capabilities", strings.NewReader("{}")))
	rec := httptest.NewRecorder()
	s.handleCapabilities(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func hasRuleField(c capabilities, name string) bool {
	for _, f := range c.Rules.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

func hasFormatField(c capabilities, path string) bool {
	for _, f := range c.Formatter.Fields {
		if f.Path == path {
			return true
		}
	}
	return false
}
