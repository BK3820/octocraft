package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"

	"github.com/azure/azure-functions-golang-worker/sdk"
	"github.com/azure/azure-functions-golang-worker/worker"
	"github.com/strawhat-enterprise/github-app-factory/internal/factory"
)

//go:embed public
var publicFiles embed.FS

func main() {
	publicFS, err := fs.Sub(publicFiles, "public")
	if err != nil {
		log.Fatalf("load embedded dashboard: %v", err)
	}
	app, err := factory.New(factory.Config{PublicFS: publicFS})
	if err != nil {
		log.Fatalf("initialize GitHub App Factory: %v", err)
	}

	functionApp := sdk.FunctionApp()
	functionApp.HTTP(
		"router",
		app.ServeHTTP,
		sdk.WithRoute("{*path}"),
		sdk.WithMethods(http.MethodGet, http.MethodPost),
		sdk.WithAuth("anonymous"),
	)
	worker.Start(functionApp)
}
