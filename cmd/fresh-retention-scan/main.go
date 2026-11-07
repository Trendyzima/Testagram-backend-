package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshstorage"
)

func main() {
	policy, err := freshstorage.RetentionPolicyFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("APPWRITE_ENDPOINT")), "/")
	project := strings.TrimSpace(os.Getenv("APPWRITE_PROJECT_ID"))
	database := strings.TrimSpace(os.Getenv("APPWRITE_DATABASE_ID"))
	key := strings.TrimSpace(os.Getenv("APPWRITE_SERVER_API_KEY"))
	if endpoint == "" || project == "" || database == "" || key == "" {
		log.Fatal("Appwrite scanner configuration is incomplete")
	}
	parseOffset := func(name string) int {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return 0
		}
		value, e := strconv.Atoi(raw)
		if e != nil || value < 0 {
			log.Fatalf("%s must be a non-negative integer", name)
		}
		return value
	}
	db, err := appwritedb.New(appwritedb.Config{Endpoint: endpoint, ProjectID: project, DatabaseID: database, APIKey: key})
	if err != nil {
		log.Fatal(err)
	}
	store, err := freshstorage.NewAppwriteStore(db)
	if err != nil {
		log.Fatal(err)
	}
	jobs, err := freshstorage.NewAppwriteRetentionJobs(db)
	if err != nil {
		log.Fatal(err)
	}
	scanner, err := freshstorage.NewRetentionScanner(store, jobs)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report, err := scanner.Scan(ctx, policy, time.Now().UTC(), freshstorage.RetentionScanCursor{CreatedOffset: parseOffset("TESTAGRAM_RETENTION_CREATED_OFFSET"), DeletedOffset: parseOffset("TESTAGRAM_RETENTION_DELETED_OFFSET")})
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(report); err != nil {
		log.Fatal(err)
	}
}
