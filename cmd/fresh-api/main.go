package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/algorithmclient"
	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshapi"
	"github.com/Trendyzima/Testagram-backend-/internal/freshfeed"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
	"github.com/Trendyzima/Testagram-backend-/internal/neondb"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), "/")
	project := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	database := strings.TrimSpace(os.Getenv("APPWRITE_DATABASE_ID"))
	key := strings.TrimSpace(os.Getenv("APPWRITE_SERVER_API_KEY"))
	if endpoint == "" || project == "" || database == "" || key == "" {
		log.Fatal("APPWRITE_ENDPOINT, APPWRITE_PROJECT_ID, APPWRITE_DATABASE_ID and APPWRITE_SERVER_API_KEY are required")
	}
	db, err := appwritedb.New(appwritedb.Config{Endpoint: endpoint, ProjectID: project, DatabaseID: database, APIKey: key})
	if err != nil {
		log.Fatal(err)
	}
	api, err := freshapi.New(db, endpoint, project, nil)
	if err != nil { log.Fatal(err) }
	if err := api.WithAppwriteServerKey(key); err != nil { log.Fatal(err) }
	if identitySecret := strings.TrimSpace(os.Getenv("IDENTITY_PREAUTH_SECRET")); identitySecret != "" {
		if err := api.WithIdentitySecret(identitySecret); err != nil { log.Fatal(err) }
	}

	neonURL := strings.TrimSpace(os.Getenv("NEON_DATABASE_URL"))
	neonRequired := strings.EqualFold(strings.TrimSpace(os.Getenv("NEON_REQUIRED")), "true")
	var neonClient *neondb.Client
	if neonURL == "" {
		if neonRequired {
			log.Fatal("NEON_REQUIRED is true but NEON_DATABASE_URL is not configured")
		}
	} else {
		neonClient, err = neondb.Open(context.Background(), neondb.Config{
			URL: neonURL, MaxOpenConns: 8, MaxIdleConns: 2,
			ConnMaxLifetime: 15 * time.Minute, ConnectTimeout: 5 * time.Second,
		})
		if err != nil {
			log.Fatalf("configure Neon social data plane: %v", err)
		}
		defer neonClient.Close()
	}
	api.WithNeon(neonClient, neonRequired)

	// Publishing remains unavailable unless the private moderation provider is
	// configured. The post handler also enforces this server-side (not just UI).
	moderationKey := strings.TrimSpace(os.Getenv("ONSPACE_AI_API_KEY"))
	if moderationKey != "" {
		moderationBase := strings.TrimSpace(os.Getenv("ONSPACE_AI_BASE_URL"))
		if moderationBase == "" {
			moderationBase = "https://ai.onspace.ai/v1"
		}
		moderation, moderationErr := freshapi.NewHTTPModerationService(moderationBase, moderationKey, nil)
		if moderationErr != nil {
			log.Fatalf("configure content moderation: %v", moderationErr)
		}
		api.WithModeration(moderation)
		log.Printf("fresh post moderation enabled")
	} else {
		log.Printf("fresh post publishing is fail-closed: ONSPACE_AI_API_KEY is not configured")
	}
	if token := strings.TrimSpace(os.Getenv("FRESH_FEDERATION_INTERNAL_TOKEN")); token != "" {
		if err := api.WithInternalToken(token); err != nil {
			log.Fatal(err)
		}
	}
	var allowedOrigins []string
	for _, origin := range strings.Split(os.Getenv("FRESH_API_ALLOWED_ORIGINS"), ",") {
		if strings.TrimSpace(origin) != "" {
			allowedOrigins = append(allowedOrigins, strings.TrimSpace(origin))
		}
	}
	if err := api.WithAllowedOrigins(allowedOrigins); err != nil {
		log.Fatal(err)
	}
	var mediaSigner freshfeed.MediaSigner
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FRESH_MEDIA_ENABLED")), "true") {
		if configured := strings.TrimSpace(os.Getenv("TESTAGRAM_STORAGE_POOL_BYTES")); configured != "" {
			poolBytes, parseErr := strconv.ParseInt(configured, 10, 64)
			if parseErr != nil || poolBytes != freshstorage.TargetPoolBytes {
				log.Fatalf("TESTAGRAM_STORAGE_POOL_BYTES must equal the supported 150 GB decimal target (%d)", freshstorage.TargetPoolBytes)
			}
		}
		providers, err := freshstorage.FromEnv()
		if err != nil {
			log.Fatal(err)
		}
		perUserQuotaBytes := int64(2 << 30)
		if configured := strings.TrimSpace(os.Getenv("TESTAGRAM_STORAGE_PER_USER_BYTES")); configured != "" {
			parsed, parseErr := strconv.ParseInt(configured, 10, 64)
			if parseErr != nil {
				log.Fatal("TESTAGRAM_STORAGE_PER_USER_BYTES must be a positive decimal byte count")
			}
			perUserQuotaBytes = parsed
		}
		store, err := freshstorage.NewAppwriteStoreWithUserQuota(db, perUserQuotaBytes)
		if err != nil {
			log.Fatal(err)
		}
		if err := store.BootstrapPlatformQuota(context.Background()); err != nil {
			log.Fatal(err)
		}
		maxObjectBytes := int64(500 << 20)
		media, err := freshstorage.NewService(store, providers, maxObjectBytes)
		if err != nil {
			log.Fatal(err)
		}
		api.WithStorage(media)
		mediaSigner = media
		log.Printf("fresh Appwrite media lifecycle enabled (B2 primary, R2 verified replica)")
	}

	var ranker *algorithmclient.Client
	algorithmEndpoint := strings.TrimSpace(os.Getenv("ALGORITHM_ENDPOINT"))
	algorithmToken := strings.TrimSpace(os.Getenv("ALGORITHM_INTERNAL_TOKEN"))
	if algorithmEndpoint == "" && algorithmToken == "" {
		log.Printf("Algorithm ranker is not configured; feed will use the safe chronological order")
	} else if algorithmEndpoint == "" || algorithmToken == "" {
		log.Printf("Algorithm ranker is disabled because ALGORITHM_ENDPOINT and ALGORITHM_INTERNAL_TOKEN must be configured together")
	} else {
		ranker, err = algorithmclient.New(algorithmEndpoint, algorithmToken, 1200*time.Millisecond)
		if err != nil {
			log.Printf("Algorithm ranker configuration rejected; continuing without ranking: %v", err)
			ranker = nil
		}
	}
	rankingRequired := strings.EqualFold(strings.TrimSpace(os.Getenv("FEED_RANKING_REQUIRED")), "true")
	if rankingRequired && ranker == nil {
		log.Printf("required ranking is unavailable; Appwrite authentication remains online and feed requests will fail closed")
	}
	feed, err := freshfeed.New(db, ranker, mediaSigner)
	if err != nil {
		log.Fatal(err)
	}
	feed.RequireRanker(rankingRequired)
	api.WithFeed(feed)

	addr := strings.TrimSpace(os.Getenv("FRESH_API_ADDR"))
	if addr == "" {
		addr = ":8081"
	}
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("fresh Appwrite API listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func runHealthcheck() int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8081/readyz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
