package utils

import (
	"context"
	"os"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// GetSheetsService creates a Google Sheets service client using a service account JSON credentials file.
func GetSheetsService(credentialsPath string) (*sheets.Service, error) {
	ctx := context.Background()
	data, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, err
	}

	config, err := google.JWTConfigFromJSON(data, sheets.SpreadsheetsScope)
	if err != nil {
		return nil, err
	}

	client := config.Client(ctx)
	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}

	return srv, nil
}

// GetDocsService creates a Google Docs service client using a service account JSON credentials file.
func GetDocsService(credentialsPath string) (*docs.Service, error) {
	ctx := context.Background()
	data, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, err
	}

	config, err := google.JWTConfigFromJSON(data, docs.DocumentsScope)
	if err != nil {
		return nil, err
	}

	client := config.Client(ctx)
	srv, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}

	return srv, nil
}
