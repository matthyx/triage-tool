package main

import (
	_ "embed"
	"strings"
	"text/template"
)

//go:embed prompts/pr-review.tmpl
var prReviewPromptSource string

var prReviewPrompt = template.Must(template.New("pr-review").Parse(prReviewPromptSource))

func renderPRReviewPrompt(prURL string) (string, error) {
	var description strings.Builder
	err := prReviewPrompt.Execute(&description, struct{ PRURL string }{PRURL: prURL})
	return description.String(), err
}
