// This file is part of the SmartTechLabs AI Workshop material.
// Contact: ai-consulting@smarttechlabs.de — https://www.smarttechlabs.de
// SmartTechLabs is also available for AI projects and consulting.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the LICENSE file in the project root or
// http://www.apache.org/licenses/LICENSE-2.0 for the full text.

package main

import "testing"

// TestIsTerminalError lives in safety_test.go.

func TestIsToolFailure(t *testing.T) {
	cases := []struct {
		name    string
		result  string
		wantHit bool
	}{
		// Empty / error prefix branch
		{"empty string", "", true},
		{"whitespace only", "   \n\t  ", true},
		{"error prefix", "error: tool exploded", true},
		{"json error prefix", `{"error": "boom"}`, true},
		{"no results", "no results found for that query", true},
		{"(no results)", "(no results)", true},
		{"(no content)", "(no content)", true},

		// Must NOT trip on prose that quotes "error" mid-sentence.
		{"prose mentioning errors", "The article discusses common errors developers make", false},

		// Real, useful short responses must pass through.
		{"short port status", "Port 8000: idle", false},
		{"short JSON ok", `{"status":"ok"}`, false},

		// Anti-bot / CAPTCHA challenge pages.
		{"google sorry redirect", "Page URL: https://www.google.com/sorry/index?continue=...", true},
		{"google unusual traffic prose", "Our systems have detected unusual traffic from your computer network.", true},
		{"captcha verify human", "Please verify you are a human before continuing.", true},
		{"cloudflare challenge", "Performance & security by Cloudflare. Cloudflare Ray ID: abc123", true},
		{"robot check", "Are you a robot? Please complete this challenge.", true},

		// Anti-bot markers must be case-insensitive.
		{"google sorry uppercase", "PAGE URL: HTTPS://WWW.GOOGLE.COM/SORRY/INDEX?...", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isToolFailure(c.result); got != c.wantHit {
				t.Errorf("isToolFailure(%q) = %v, want %v", c.result, got, c.wantHit)
			}
		})
	}
}
