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
	objectID := strings.TrimSpace(os.Getenv("TESTAGRAM_REPAIR_OBJECT_ID"))
	if objectID == "" {
		log.Fatal("TESTAGRAM_REPAIR_OBJECT_ID is required")
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Hour)
	defer cancel()
	if err := service.RepairObject(ctx, objectID); err != nil {
		log.Fatal(err)
	}
	log.Printf("media object %s: B2 and R2 replicas verified", objectID)
}
