package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	apiKeyID := os.Getenv("POLY_API_KEY_ID")
	privKeyB64 := os.Getenv("POLY_PRIVATE_KEY_B64")

	privKeyBytes, err := base64.StdEncoding.DecodeString(privKeyB64)
	if err != nil {
		log.Fatal(err)
	}
	privKey := ed25519.PrivateKey(privKeyBytes)

	endpoints := []struct {
		name   string
		method string
		path   string
	}{
		{"POSITIONS", "GET", "/v1/account/positions"},
		{"OPEN ORDERS", "GET", "/v1/orders/open"},
	}

	for _, ep := range endpoints {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		msg := ts + ep.method + ep.path
		sig := ed25519.Sign(privKey, []byte(msg))
		sigB64 := base64.StdEncoding.EncodeToString(sig)

		req, _ := http.NewRequest(ep.method, "https://api.polymarket.us"+ep.path, nil)
		req.Header.Set("X-PM-Access-Key", apiKeyID)
		req.Header.Set("X-PM-Timestamp", ts)
		req.Header.Set("X-PM-Signature", sigB64)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("=== %s === ERROR: %v\n", ep.name, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		fmt.Printf("=== %s (HTTP %d) ===\n", ep.name, resp.StatusCode)
		s := string(body)
		if len(s) > 5000 {
			s = s[:5000] + "...(truncated)"
		}
		fmt.Println(s)
		fmt.Println()
	}
}
