package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Trendyzima/Testagram-backend-/internal/appwritedb"
	"github.com/Trendyzima/Testagram-backend-/internal/freshapi"
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
	addr := strings.TrimSpace(os.Getenv("FRESH_API_ADDR"))
	if addr == "" {
		addr = ":8081"
	}
	server := &http.Server{Addr: addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("fresh Appwrite API listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}
