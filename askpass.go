package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// runAskpass runs when ssh starts this binary as SSH_ASKPASS: it forwards
// the prompt to the running app, which asks the user, and prints the answer.
func runAskpass() {
	body, _ := json.Marshal(map[string]string{
		"host":   os.Getenv("GANTRY_ASKPASS_HOST"),
		"prompt": strings.Join(os.Args[1:], " "),
	})
	req, err := http.NewRequest(http.MethodPost, os.Getenv("GANTRY_ASKPASS_URL"), bytes.NewReader(body))
	if err != nil {
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("GANTRY_ASKPASS_TOKEN"))
	resp, err := (&http.Client{Timeout: 4 * time.Minute}).Do(req)
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool   `json:"ok"`
		Answer string `json:"answer"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || !out.OK {
		os.Exit(1)
	}
	fmt.Println(out.Answer)
	os.Exit(0)
}
