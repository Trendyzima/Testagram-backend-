package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshapi"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

func main() {
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
	if err != nil {
		log.Fatal(err)
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
		log.Printf("fresh Appwrite media lifecycle enabled (B2 primary, R2 verified replica)")
	}
	addr := strings.TrimSpace(os.Getenv("FRESH_API_ADDR"))
	if addr == "" {
		addr = ":8081"
	}
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("fresh Appwrite API listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
