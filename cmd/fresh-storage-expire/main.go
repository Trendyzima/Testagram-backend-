package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

func main() {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), "/")
	project := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	database := strings.TrimSpace(os.Getenv("APPWRITE_DATABASE_ID"))
	key := strings.TrimSpace(os.Getenv("APPWRITE_SERVER_API_KEY"))
	if endpoint == "" || project == "" || database == "" || key == "" {
		log.Fatal("Appwrite server configuration is incomplete")
	}
	db, err := appwritedb.New(appwritedb.Config{Endpoint: endpoint, ProjectID: project, DatabaseID: database, APIKey: key})
	if err != nil {
		log.Fatal(err)
	}
	providers, err := freshstorage.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	store, err := freshstorage.NewAppwriteStore(db)
	if err != nil {
		log.Fatal(err)
	}
	service, err := freshstorage.NewService(store, providers, 500<<20)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	expired, err := store.ListExpiredReservations(ctx, time.Now().UTC(), 100)
	if err != nil {
		log.Fatal(err)
	}
	var failed int
	for _, reservation := range expired {
		if err := service.ExpireReservation(ctx, reservation.ID); err != nil {
			failed++
			log.Printf("reservation %s cleanup deferred: %v", reservation.ID, err)
		}
	}
	log.Printf("expiry sweep: found=%d failed=%d", len(expired), failed)
	if failed > 0 {
		os.Exit(1)
	}
}
