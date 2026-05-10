package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

const tokenFileName = "token.json"

type subscription struct {
	ID           string
	ChannelID    string
	ChannelTitle string
}

func main() {
	ctx := context.Background()

	credentialsPath := flag.String("credentials", "credentials.json", "OAuth client credentials JSON downloaded from Google Cloud")
	tokenPath := flag.String("token", tokenFileName, "OAuth token cache path")
	execute := flag.Bool("execute", false, "Actually unsubscribe. Without this flag the tool only prints what it would remove")
	yes := flag.Bool("yes", false, "Skip the interactive confirmation prompt when used with --execute")
	limit := flag.Int("limit", 0, "Maximum number of subscriptions to process; 0 means all")
	flag.Parse()

	logger := log.New(os.Stderr, "", 0)

	service, err := youtubeService(ctx, *credentialsPath, *tokenPath)
	if err != nil {
		logger.Fatalf("authentication failed: %v", err)
	}

	subs, err := listSubscriptions(service, *limit)
	if err != nil {
		logger.Fatalf("listing subscriptions failed: %v", err)
	}
	if len(subs) == 0 {
		fmt.Println("No subscriptions found.")
		return
	}

	for _, sub := range subs {
		fmt.Printf("%s\t%s\t%s\n", sub.ID, sub.ChannelID, sub.ChannelTitle)
	}

	if !*execute {
		fmt.Printf("\nDry run: found %d subscriptions. Re-run with --execute to unsubscribe.\n", len(subs))
		return
	}

	if !*yes {
		if err := confirm(len(subs)); err != nil {
			logger.Fatal(err)
		}
	}

	failures := 0
	for i, sub := range subs {
		fmt.Printf("[%d/%d] unsubscribing from %q... ", i+1, len(subs), sub.ChannelTitle)
		err := service.Subscriptions.Delete(sub.ID).Do()
		if err != nil {
			failures++
			fmt.Printf("failed: %v\n", err)
			continue
		}
		fmt.Println("ok")
	}

	if failures > 0 {
		logger.Fatalf("finished with %d failures", failures)
	}
	fmt.Printf("Unsubscribed from %d channels.\n", len(subs))
}

func youtubeService(ctx context.Context, credentialsPath, tokenPath string) (*youtube.Service, error) {
	credentials, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", credentialsPath, err)
	}

	config, err := google.ConfigFromJSON(credentials, youtube.YoutubeForceSslScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	token, err := tokenFromFile(tokenPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		token, err = tokenFromWeb(ctx, config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenPath, token); err != nil {
			return nil, err
		}
	}

	client := config.Client(ctx, token)
	return youtube.NewService(ctx, option.WithHTTPClient(client))
}

func tokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start local callback listener: %w", err)
	}
	defer listener.Close()

	state, err := randomState()
	if err != nil {
		return nil, err
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}

	config.RedirectURL = "http://" + listener.Addr().String() + "/oauth2callback"
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid OAuth state", http.StatusBadRequest)
			errCh <- errors.New("invalid OAuth state")
			return
		}
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			http.Error(w, oauthErr, http.StatusBadRequest)
			errCh <- fmt.Errorf("OAuth error: %s", oauthErr)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			errCh <- errors.New("missing authorization code")
			return
		}
		fmt.Fprintln(w, "Authorization complete. You can return to the terminal.")
		codeCh <- code
	})

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	defer server.Shutdown(ctx)

	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("Open this URL in your browser and authorize access:\n\n%s\n\n", authURL)

	select {
	case code := <-codeCh:
		token, err := config.Exchange(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("exchange authorization code: %w", err)
		}
		return token, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for OAuth callback")
	}
}

func listSubscriptions(service *youtube.Service, limit int) ([]subscription, error) {
	var subs []subscription
	pageToken := ""

	for {
		call := service.Subscriptions.List([]string{"snippet"}).Mine(true).MaxResults(50)
		if pageToken != "" {
			call.PageToken(pageToken)
		}

		response, err := call.Do()
		if err != nil {
			return nil, err
		}

		for _, item := range response.Items {
			sub := subscription{ID: item.Id}
			if item.Snippet != nil {
				sub.ChannelTitle = item.Snippet.Title
				if item.Snippet.ResourceId != nil {
					sub.ChannelID = item.Snippet.ResourceId.ChannelId
				}
			}
			subs = append(subs, sub)
			if limit > 0 && len(subs) >= limit {
				return subs, nil
			}
		}

		if response.NextPageToken == "" {
			return subs, nil
		}
		pageToken = response.NextPageToken
	}
}

func confirm(count int) error {
	fmt.Printf("\nThis will unsubscribe from %d YouTube channels. Type UNSUBSCRIBE to continue: ", count)
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		return err
	}
	if answer != "UNSUBSCRIBE" {
		return errors.New("confirmation did not match; aborting")
	}
	return nil
}

func tokenFromFile(path string) (*oauth2.Token, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var token oauth2.Token
	if err := json.NewDecoder(file).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &token, nil
}

func saveToken(path string, token *oauth2.Token) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create token directory: %w", err)
		}
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(token); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func randomState() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
