package main

// Outbound notifications (integration). When a webhook URL is configured,
// PhotoShare POSTs a short message to it on events like new uploads. Supports
// ntfy (self-hosted or ntfy.sh) and Discord out of the box; any other URL gets
// a plain-text POST, which most webhook receivers accept.
//
// This is fire-and-forget and best-effort: a failing or slow webhook never
// blocks or fails the action that triggered it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

var notifyURL string // configured webhook (AppConfig.NotifyURL); empty = disabled

func notifyEnabled() bool { return notifyURL != "" }

var notifyHTTP = &http.Client{Timeout: 8 * time.Second}

func isDiscordWebhook(u string) bool {
	return strings.Contains(u, "discord.com/api/webhooks") ||
		strings.Contains(u, "discordapp.com/api/webhooks")
}

// sendNotify posts a single notification to url synchronously and reports the
// error (used by the "Test" button so the admin gets immediate feedback).
func sendNotify(url, title, msg string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	var req *http.Request
	var err error
	if isDiscordWebhook(url) {
		body, _ := json.Marshal(map[string]string{"content": title + " — " + msg})
		req, err = http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		// ntfy and generic receivers: plain-text body. ntfy reads the Title
		// header for the notification title.
		req, err = http.NewRequest(http.MethodPost, url, strings.NewReader(msg))
		if err != nil {
			return err
		}
		req.Header.Set("Title", title)
	}
	resp, err := notifyHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// notify fires a notification in the background to the configured webhook.
// No-op (and cheap) when notifications aren't configured.
func notify(title, msg string) {
	if !notifyEnabled() {
		return
	}
	url := notifyURL
	go func() {
		if err := sendNotify(url, title, msg); err != nil {
			log.Printf("[NOTIFY] failed: %v", err)
		}
	}()
}

// notifyInit records the configured webhook. Called once from main().
func notifyInit(url string) {
	notifyURL = strings.TrimSpace(url)
	if notifyEnabled() {
		kind := "webhook"
		if isDiscordWebhook(notifyURL) {
			kind = "Discord"
		}
		log.Printf("[NOTIFY] enabled (%s)", kind)
	}
}

// POST /api/admin/notify-test {url} — send a test notification so the admin can
// verify the webhook before saving. Falls back to the saved URL if none given.
func notifyTestHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	url := strings.TrimSpace(body.URL)
	if url == "" {
		url = notifyURL
	}
	if err := sendNotify(url, "PhotoShare", "Test notification — your webhook is working."); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
